package chaos

import (
	"strings"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

func TestNetemScript(t *testing.T) {
	f := scenario.Fault{Type: scenario.FaultLatency, Params: map[string]string{scenario.ParamLatency: "250", scenario.ParamJitter: "20"}}
	if s := NetemScript(f); !strings.Contains(s, "netem delay 250ms 20ms") {
		t.Errorf("latência: %s", s)
	}
	f = scenario.Fault{Type: scenario.FaultPacketLoss}
	if s := NetemScript(f); !strings.Contains(s, "netem loss 10%") {
		t.Errorf("perda com padrão: %s", s)
	}
}

func TestStressArgs(t *testing.T) {
	f := scenario.Fault{Type: scenario.FaultCPUStress, DurationSeconds: 30, Params: map[string]string{scenario.ParamCPULoad: "90"}}
	got := strings.Join(StressArgs(f), " ")
	if got != "cpu -load 90 -workers 1 -timeout 35s" {
		t.Errorf("cpu: %s", got)
	}
	f = scenario.Fault{Type: scenario.FaultMemStress, DurationSeconds: 10, Params: map[string]string{scenario.ParamMemBytes: "80%"}}
	if got := strings.Join(StressArgs(f), " "); got != "mem -bytes 80% -timeout 15s" {
		t.Errorf("mem: %s", got)
	}
}
