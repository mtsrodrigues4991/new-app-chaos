package load

import (
	"strings"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

func TestScript(t *testing.T) {
	s := scenario.Scenario{
		Target:    scenario.Target{Container: "demo-api", BaseURL: "http://demo-api:8080/"},
		Endpoints: []scenario.Endpoint{{Method: "get", Path: "/items"}},
		Profile:   scenario.Profile{Type: scenario.ProfileRamp, VUs: 50, DurationSeconds: 30},
	}
	js, err := Script(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"http://demo-api:8080"`, `"duration":"30s","target":50`, `"path":"/items"`, `"method":"GET"`, `"weight":1`} {
		if !strings.Contains(js, want) {
			t.Fatalf("script não contém %s:\n%s", want, js)
		}
	}
}
