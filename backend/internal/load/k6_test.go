package load

import (
	"strings"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

func TestScript(t *testing.T) {
	s := scenario.Scenario{
		Endpoints: []scenario.Endpoint{{Method: "GET", Path: "/items", Weight: 1}},
		Stages:    []scenario.Stage{{Duration: 30 * time.Second, Target: 50}},
	}
	js, err := Script("http://demo-api:8080", s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"http://demo-api:8080"`, `"duration":"30s","target":50`, `"path":"/items"`} {
		if !strings.Contains(js, want) {
			t.Fatalf("script não contém %s:\n%s", want, js)
		}
	}
}
