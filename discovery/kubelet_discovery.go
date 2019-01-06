package discovery

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nitro/sidecar/service"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/relistan/go-director"
	log "github.com/sirupsen/logrus"

	// We're stuck on V1 until the Kubelet is updated
	api "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/client-go/kubernetes/scheme"
)

const (
	KubeletTimeout       = 1500 * time.Millisecond // Includes data fetch as well
	KubeletHeaderTimeout = 500 * time.Millisecond  // Time until first header
)

// KubeletClient is an interface used for mocking out the Kubelet in testing
// or for substituting differnt implementations.
type KubeletClient interface {
	ListPods() (*api.PodList, error)
	Ping() error
}

type kubeletClient struct {
	url    string
	client *http.Client
}

// NewKubeletClient returns a properly configured real-world client to talk to
// the kubelet.
func NewKubeletClient(url string) KubeletClient {
	transport := cleanhttp.DefaultPooledTransport()
	// Kubelet uses self-signed certs by default
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	transport.ResponseHeaderTimeout = KubeletHeaderTimeout

	return &kubeletClient{
		url: url,
		client: &http.Client{
			Transport: transport,
			Timeout:   KubeletTimeout,
		},
	}
}

// ListPods connects directly to the kubelet API on the SSL port (normally) and
// deserializes the PodList.
func (k *kubeletClient) ListPods() (*api.PodList, error) {
	resp, err := k.client.Get(k.url)
	if err != nil {
		return nil, fmt.Errorf("Unable to fetch from Kubelet: %s", err)
	}

	var pl api.PodList

	// Just read it all into memory
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Unable to read from Kubelet: %s", err)
	}

	s := json.NewSerializer(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, false)
	_, _, err = s.Decode(data, nil, &pl)

	return &pl, nil
}

// Ping allows the higher level routines to know that the kubelet is still alive.
func (k *kubeletClient) Ping() error {
	resp, err := k.client.Head(k.url)
	if err != nil {
		return err
	}

	if resp.ContentLength < 1 {
		return errors.New("Bad response from Kubelet")
	}

	return nil
}

type nameFunc func(*api.Pod) string

// KubeletDiscovery is an implementation of Sidecar's discovery system, backed direcly
// by the Kubernetes kubelet API. This allows Sidecar to continue to run in a distributed,
// partition-tolerant manner, relying on the local state of the Kubelet rather than
// the centralized state of the API server.
type KubeletDiscovery struct {
	url          string              // The Kubelet URL
	services     []*service.Service  // The list of services we know about
	Namer        nameFunc            // How we determine the name from the Pod
	client       KubeletClient       // The Kubelet client we'll use
	advertiseIP  string              // The address we'll advertise for services
	healthChecks map[string][]string // Cached health checks for services
	sync.RWMutex                     // Reader/Writer lock
}

// defaultNamer is the default implementation of the mechanism to convert K8s
// Pod names into Sidecar service names. It can be swapped out as a
// configuration item on the KubeletDiscovery struct.
func defaultNamer(p *api.Pod) string {
	// Use a Label first, fall back to pod name
	if name, ok := p.Labels["sidecar.ServiceName"]; ok {
		return name
	}

	// This is not great. Look at better solutions if this is an issue.
	return strings.Replace(p.Name, "-"+p.Spec.NodeName, "", 1)
}

// NewKubeletDiscovery returns a properly configured KubeletDiscovery that is
// ready to use immediately.
func NewKubeletDiscovery(url string, ip string) *KubeletDiscovery {
	discovery := KubeletDiscovery{
		url:          url,
		client:       NewKubeletClient(url),
		advertiseIP:  ip,
		healthChecks: make(map[string][]string),
		Namer:        defaultNamer,
	}

	return &discovery
}

// Services returns the slice of services we found running. It does not connect
// to the kubelet, it relies on the cached data from the last run of the
// getServices() function in the main Run() loop.
func (d *KubeletDiscovery) Services() []service.Service {
	d.RLock()
	defer d.RUnlock()

	svcList := make([]service.Service, len(d.services))

	for i, svc := range d.services {
		svcList[i] = *svc
	}

	return svcList
}

// getServices runs on the background looper, then fetches pods and converts
// them into a service list. This is then stored in the Discovery for use on
// the next call.
func (d *KubeletDiscovery) getServices() {
	pods, err := d.client.ListPods()
	if err != nil {
		log.Errorf("Error fetching pods from Kubelet: %s", err)
		return
	}

	d.Lock()
	defer d.Unlock()

	// We'll swap out the service list with the new one
	d.services = make([]*service.Service, len(pods.Items))
	for i, pod := range pods.Items {
		svc := d.PodToService(&pod)
		d.services[i] = svc
		d.healthChecks[svc.ID] = healthCheckFor(&pod)
	}
}

// PodToService converts a k8s Pod to a Sidecar Service.
func (d *KubeletDiscovery) PodToService(pod *api.Pod) *service.Service {
	var svc service.Service

	uid := string(pod.UID)
	seenTime, _ := time.Parse(time.RFC3339, pod.Annotations["kubernetes.io/config.seen"])

	var image string
	image = pod.Spec.Containers[0].Image
	if !strings.Contains(image, ":") {
		image = image + ":latest"
	}

	svc.ID = uid[:12] // Use short IDs
	svc.Name = d.Namer(pod)
	svc.Image = image
	svc.Created = seenTime
	svc.Updated = time.Now().UTC()
	svc.Hostname = pod.Spec.NodeName
	svc.Status = service.ALIVE

	if mode, ok := pod.Annotations["relistan.com/sidecar.ProxyMode"]; ok {
		svc.ProxyMode = mode
	} else {
		svc.ProxyMode = "http"
	}

	svc.Ports = make([]service.Port, 0)
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.HostPort != 0 {
				svc.Ports = append(svc.Ports, buildPortFor(&port, pod, d.advertiseIP))
			}
		}
	}

	return &svc
}

// healthCheckFor will attempt to mimic the Kubernetes readiness probe, fall back
// to the livenese probe, or return nothing otherwise. Currently only implements
// HttpGet health checks since the K8s command checks require running code in the
// container. None of this would be necessary if the kubelet API actually exposed
// the health checking state.
func healthCheckFor(pod *api.Pod) []string {
	container := pod.Spec.Containers[0]

	// Allow override of any other behavior by use of Annotations
	if check, ok := pod.Annotations["relistan.com/sidecar.HealthCheck"]; ok {
		if args, ok := pod.Annotations["relistan.com/sidecar.HealthCheckArgs"]; ok {
			return []string{check, args}
		}
	}

	// Prefer readiness probe when possible
	if container.ReadinessProbe != nil {
		if container.ReadinessProbe.HTTPGet != nil {
			args := healthUrlFor(&container, pod.Spec.NodeName, container.ReadinessProbe)
			return []string{"HttpGet", args}
		}
	} else if container.LivenessProbe != nil {
		if container.LivenessProbe.HTTPGet != nil {
			args := healthUrlFor(&container, pod.Spec.NodeName, container.LivenessProbe)
			return []string{"HttpGet", args}
		}
	}

	return []string{"", ""}
}

// healthUrlFor turns the funky K8s health check URL format into something we
// can pass to the healthy library for checking.
func healthUrlFor(container *api.Container, nodeName string, handler *api.Probe) string {
	hostPort := handler.HTTPGet.Host
	if len(hostPort) < 1 {
		hostPort = nodeName
	}

	port := handler.HTTPGet.Port.IntValue()
	portStr := handler.HTTPGet.Port.String()
	if port > 0 {
		hostPort = hostPort + ":" + portStr
	} else {
		for _, p := range container.Ports {
			if p.Name == portStr {
				hostPort = fmt.Sprintf("%s:%d", hostPort, p.HostPort)
				break
			}
		}

	}

	hUrl := url.URL{
		Scheme: strings.ToLower(string(handler.HTTPGet.Scheme)),
		Host:   hostPort,
		Path:   handler.HTTPGet.Path,
	}
	return hUrl.String()
}

// buildPortFor uses annotations and Pod data from k8s to return a Sidecar
// port configured for this service.
func buildPortFor(port *api.ContainerPort, pod *api.Pod, ip string) service.Port {
	if pod == nil || port == nil {
		return service.Port{}
	}

	// Look up service port annotations in the format "sidecar.ServicePort_80=8080"
	svcPortLabel := fmt.Sprintf("relistan.com/sidecar.ServicePort_%d", port.ContainerPort)

	// Use the IP we passed as a default, but prefer the one on the Pod
	if len(port.HostIP) > 0 {
		ip = port.HostIP
	}

	returnPort := service.Port{
		Port: int64(port.HostPort),
		Type: string(port.Protocol),
		IP:   ip,
	}

	if svcPort, ok := pod.Annotations[svcPortLabel]; ok {
		svcPortInt, err := strconv.Atoi(svcPort)
		if err != nil {
			log.Errorf("Error converting label value for %s to integer: %s",
				svcPortLabel,
				err.Error(),
			)
			return returnPort
		}

		// Everything was good, set the service port
		returnPort.ServicePort = int64(svcPortInt)
	}

	return returnPort
}

// HealthCheck returns a pair of strings defining the health check that Sidecar
// should use for the service in question.
func (d *KubeletDiscovery) HealthCheck(svc *service.Service) (string, string) {
	d.RLock()
	defer d.RUnlock()

	check, ok := d.healthChecks[svc.ID]
	if !ok {
		log.Warnf("Unable to find health check for kubelet service %s", svc.ID)
		return "", ""
	}

	return check[0], check[1]
}

// Listeners returns the list of Pods that want to receive Sidecar events. This
// is not currently implemented.
func (d *KubeletDiscovery) Listeners() []ChangeListener {
	// TODO inspect pod annotations and generate list of listeners,
	// following patter from docker_discovery.go
	return []ChangeListener{}
}

// Run is the main run loop that uses a looper to retrieve data from the kubelet
// on a timed basis.
func (d *KubeletDiscovery) Run(looper director.Looper) {
	go func() {
		// Loop around, process any events which came in, and
		// periodically fetch the whole pod list
		looper.Loop(func() error {
			d.getServices()
			time.Sleep(DefaultSleepInterval)
			return nil
		})
	}()
}
