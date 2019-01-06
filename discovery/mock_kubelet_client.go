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

	return &pl, err
}

func (m *mockKubeletClient) Ping() error {
	if m.ShouldPingError {
		return errors.New("OMG ping error")
	}

	return nil
}
