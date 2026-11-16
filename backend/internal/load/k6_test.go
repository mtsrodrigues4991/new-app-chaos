package load

import (
	"strings"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

func script(t *testing.T, s scenario.Scenario) string {
	t.Helper()
	js, err := Script(s)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func wantAll(t *testing.T, js string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(js, want) {
			t.Fatalf("script não contém %s:\n%s", want, js)
		}
	}
}

func TestScript(t *testing.T) {
	js := script(t, scenario.Scenario{
		Target:    scenario.Target{Container: "demo-api", BaseURL: "http://demo-api:8080/"},
		Endpoints: []scenario.Endpoint{{Method: "get", Path: "/items"}},
		Profile:   scenario.Profile{Type: scenario.ProfileRamp, VUs: 50, DurationSeconds: 30},
	})
	wantAll(t, js, `"http://demo-api:8080"`, `"stages":[{"duration":"30s","target":50}]`, `"path":"/items"`, `"method":"GET"`, `"weight":1`,
		`new Counter('status_5xx')`, `const WARMUP_MS = 0;`)
	// O modelo fechado continua como era: stages de VUs, sem executor de taxa.
	if strings.Contains(js, "arrival-rate") || strings.Contains(js, "scenarios") {
		t.Fatalf("perfil em VUs não pode usar taxa de chegada:\n%s", js)
	}
}

// No modelo aberto a taxa não depende da latência: executor de taxa de chegada, com os
// trechos em requisições por segundo e todos os VUs pré-alocados.
func TestScriptOpenModel(t *testing.T) {
	js := script(t, scenario.Scenario{
		Target:    scenario.Target{Container: "demo-api", BaseURL: "http://demo-api:8080"},
		Endpoints: []scenario.Endpoint{{Method: "GET", Path: "/items", Weight: 3}, {Method: "POST", Path: "/orders", Weight: 1}},
		Profile:   scenario.Profile{Type: scenario.ProfilePlateau, VUs: 80, Rate: 200, DurationSeconds: 60, WarmupSeconds: 15},
	})
	wantAll(t, js, `"executor":"ramping-arrival-rate"`, `"timeUnit":"1s"`, `"startRate":0`, `"preAllocatedVUs":80`, `"maxVUs":80`,
		`{"duration":"48s","target":200}`, `const WARMUP_MS = 15000;`,
		`new Trend('steady_duration', true)`, `new Trend('ep_' + i + '_duration', true)`, `exec.instance.currentTestRunDuration >= WARMUP_MS`)
}
