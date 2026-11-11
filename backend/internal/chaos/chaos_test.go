package chaos

import (
	"strings"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

func TestCommandsLatency(t *testing.T) {
	apply, revert, err := Commands(scenario.Fault{Type: scenario.FaultLatency, Params: map[string]string{"delayMs": "250"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(apply, " "); !strings.Contains(got, "netem delay 250ms") {
		t.Fatalf("comando inesperado: %s", got)
	}
	if strings.Join(revert, " ") != "tc qdisc del dev eth0 root" {
		t.Fatalf("revert inesperado: %v", revert)
	}
}

func TestCommandsUnknown(t *testing.T) {
	if _, _, err := Commands(scenario.Fault{Type: "nope"}); err == nil {
		t.Fatal("esperava erro para falha desconhecida")
	}
}
