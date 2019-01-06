package discovery

import (
	"encoding/json"
	"errors"

	api "k8s.io/api/core/v1"
)

type mockKubeletClient struct {
	PodsResponse    string
	ShouldPingError bool
	ShouldListPodsError bool
	ShouldNotHaveReadiness bool
	ShouldNotHaveLiveness bool
	ShouldHaveCheckAnnotations bool
	ShouldSkipDiscovery bool
}

func (m *mockKubeletClient) ListPods() (*api.PodList, error) {
	if m.ShouldListPodsError {
		return nil, errors.New("Intentional error")
	}

	var pl api.PodList
	err := json.Unmarshal([]byte(m.PodsResponse), &pl)

	if m.ShouldNotHaveReadiness {
		pl.Items[0].Spec.Containers[0].ReadinessProbe = nil
	}

	if m.ShouldNotHaveLiveness {
		pl.Items[0].Spec.Containers[0].LivenessProbe = nil
	}

	if m.ShouldHaveCheckAnnotations {
		pl.Items[0].Annotations["relistan.com/sidecar.HealthCheck"] = "Special"
		pl.Items[0].Annotations["relistan.com/sidecar.HealthCheckArgs"] = "args"
	}

	if m.ShouldSkipDiscovery {
		pl.Items[0].Annotations["relistan.com/sidecar.Discover"] = "false"
	}

	return &pl, err
}

func (m *mockKubeletClient) Ping() error {
	if m.ShouldPingError {
		return errors.New("OMG ping error")
	}

	return nil
}
