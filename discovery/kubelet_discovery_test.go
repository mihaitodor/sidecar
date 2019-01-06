package discovery

import (
	"testing"
	"time"

	"github.com/Nitro/sidecar/service"
	. "github.com/smartystreets/goconvey/convey"
)

var (
	SinglePodResponse = `
{
   "kind" : "PodList",
   "items" : [
      {
         "metadata" : {
            "annotations" : {
               "kubernetes.io/config.source" : "file",
               "kubernetes.io/config.seen" : "2019-01-05T12:52:39.594150917Z",
               "kubernetes.io/config.hash" : "0a850edbcf3323bdf8ea88ef3d963118",
               "relistan.com/sidecar.ServiceName" : "chaucer",
               "relistan.com/sidecar.ServicePort_80" : "10001"
            },
            "name" : "chaucer-docker1",
            "labels" : {
               "role" : "myrole"
            },
            "namespace" : "default",
            "creationTimestamp" : null,
            "uid" : "0a850edbcf3323bdf8ea88ef3d963118",
            "selfLink" : "/api/v1/namespaces/default/pods/chaucer-docker1"
         },
         "spec" : {
            "dnsPolicy" : "ClusterFirst",
            "tolerations" : [
               {
                  "operator" : "Exists",
                  "effect" : "NoExecute"
               }
            ],
            "securityContext" : {},
            "enableServiceLinks" : true,
            "restartPolicy" : "Always",
            "terminationGracePeriodSeconds" : 30,
            "nodeName" : "docker1",
            "containers" : [
               {
                  "livenessProbe" : {
                     "successThreshold" : 1,
                     "timeoutSeconds" : 1,
                     "failureThreshold" : 3,
                     "periodSeconds" : 5,
                     "httpGet" : {
                        "port" : "http",
                        "path" : "/liveness",
                        "scheme" : "HTTP"
                     },
                     "initialDelaySeconds" : 5
                  },
                  "readinessProbe" : {
                     "successThreshold" : 1,
                     "timeoutSeconds" : 1,
                     "failureThreshold" : 3,
                     "periodSeconds" : 5,
                     "httpGet" : {
                        "port" : "http",
                        "path" : "/",
                        "scheme" : "HTTP"
                     },
                     "initialDelaySeconds" : 5
                  },
                  "image" : "chaucer",
                  "imagePullPolicy" : "Always",
                  "terminationMessagePath" : "/dev/termination-log",
                  "terminationMessagePolicy" : "File",
                  "resources" : {},
                  "name" : "chaucer",
                  "ports" : [
                     {
                        "containerPort" : 80,
                        "protocol" : "TCP",
                        "name" : "http",
                        "hostPort" : 20001
                     }
                  ]
               }
            ],
            "schedulerName" : "default-scheduler"
         },
         "status" : {
            "phase" : "Pending"
         }
      }
   ],
   "apiVersion" : "v1",
   "metadata" : {}
}
`
)

func Test_NewKubeletDiscovery(t *testing.T) {
	Convey("NewKubeletDiscovery()", t, func() {
		url := "http://beowulf:10250"
		ip := "127.0.0.1"

		Convey("returns a properly configured struct", func() {
			kube := NewKubeletDiscovery(url, ip)

			So(kube.url, ShouldEqual, url)
			So(kube.advertiseIP, ShouldEqual, ip)
		})
	})
}

func Test_HealthCheck(t *testing.T) {
	Convey("HealthCheck()", t, func() {
		url := "http://beowulf:10250"
		ip := "127.0.0.1"

		mockClient := &mockKubeletClient{
			PodsResponse:    SinglePodResponse,
			ShouldPingError: false,
		}

		disco := NewKubeletDiscovery(url, ip)
		disco.client = mockClient

		Convey("returns a correctly configured health check", func() {
			disco.getServices()
			check, args := disco.HealthCheck(&service.Service{ID: "0a850edbcf33"})

			So(check, ShouldEqual, "HttpGet")
			So(args, ShouldEqual, "http://docker1:20001/")
		})

		Convey("returns a liveness check if the readiness is missing", func() {
			mockClient.ShouldNotHaveReadiness = true
			disco.getServices()

			check, args := disco.HealthCheck(&service.Service{ID: "0a850edbcf33"})

			So(check, ShouldEqual, "HttpGet")
			So(args, ShouldEqual, "http://docker1:20001/liveness")
		})

		Convey("returns empty strings if neither exists", func() {
			mockClient.ShouldNotHaveReadiness = true
			mockClient.ShouldNotHaveLiveness = true
			disco.getServices()

			check, args := disco.HealthCheck(&service.Service{ID: "0a850edbcf33"})

			So(check, ShouldEqual, "")
			So(args, ShouldEqual, "")
		})

		Convey("prefers annotations if provided", func() {
			mockClient.ShouldHaveCheckAnnotations = true
			disco.getServices()

			check, args := disco.HealthCheck(&service.Service{ID: "0a850edbcf33"})

			So(check, ShouldEqual, "Special")
			So(args, ShouldEqual, "args")
		})
	})
}

func Test_KubeletServices(t *testing.T) {
	Convey("Services()", t, func() {
		url := "http://beowulf:10250"
		ip := "127.0.0.1"
		created, _ := time.Parse(time.RFC3339, "2019-01-05T12:52:39.594150917Z")

		mockClient := &mockKubeletClient{
			PodsResponse:    SinglePodResponse,
			ShouldPingError: false,
		}

		disco := NewKubeletDiscovery(url, ip)
		disco.client = mockClient

		Convey("returns an empty list on error", func() {
			mockClient.ShouldListPodsError = true
			disco.getServices()

			svcs := disco.Services()

			So(len(svcs), ShouldEqual, 0)
		})

		Convey("returns the list of services", func() {
			disco.getServices()
			svcs := disco.Services()

			So(svcs, ShouldNotBeEmpty)
			So(svcs[0], ShouldNotBeNil)
		})

		Convey("returns a properly formatted service", func() {
			disco.getServices()
			svcs := disco.Services()
			So(svcs, ShouldNotBeEmpty)
			svc := svcs[0]

			// Check the main fields
			So(svc.ID, ShouldEqual, "0a850edbcf33")
			So(svc.Name, ShouldEqual, "chaucer")
			So(svc.Image, ShouldEqual, "chaucer:latest")
			So(svc.Created, ShouldEqual, created)
			So(svc.Updated.Sub(svc.Created), ShouldBeGreaterThan, 1*time.Hour)
			So(svc.Hostname, ShouldEqual, "docker1")
			So(svc.Status, ShouldEqual, service.ALIVE)
			So(svc.ProxyMode, ShouldEqual, "http")

			// Check the ports
			So(len(svc.Ports), ShouldEqual, 1)
			port := svc.Ports[0]
			So(port.Port, ShouldEqual, 20001)
			So(port.ServicePort, ShouldEqual, 10001)
			So(port.IP, ShouldEqual, "127.0.0.1")
		})

		Convey("skips discovery of intentionally excluded pods", func() {
			mockClient.ShouldSkipDiscovery = true
			disco.getServices()
			svcs := disco.Services()
			So(svcs, ShouldBeEmpty)
		})
	})
}
