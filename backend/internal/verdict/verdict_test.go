package verdict

import (
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

func f(v float64) *float64 { return &v }

// win cria uma janela de carga: p95 em ms e erro de 0 a 1; p95 < 0 = segundo preso.
func win(t int64, p95, errRate float64) load.Window {
	if p95 < 0 {
		return load.Window{T: t, VUs: 10, Stalled: true}
	}
	return load.Window{T: t, RPS: 100, VUs: 10, P95: f(p95), ErrorRate: f(errRate)}
}

func TestLoadOnlyAllGood(t *testing.T) {
	sc := scenario.Scenario{Mode: scenario.ModeLoad}
	var ws []load.Window
	for i := int64(0); i < 30; i++ {
		ws = append(ws, win(i, 100, 0))
	}
	v := Evaluate(sc, ws, nil)
	if v.Score != 100 || v.Grade != GradeResilient || len(v.Checks) != 2 || !v.Checks[0].Pass || !v.Checks[1].Pass {
		t.Fatalf("veredito inesperado: %+v", v)
	}
}

func TestKillWithRecovery(t *testing.T) {
	sc := scenario.Scenario{Mode: scenario.ModeBoth, Faults: []scenario.Fault{{ID: "k", Type: scenario.FaultKillRestart}}}
	var ws []load.Window
	for i := int64(0); i < 60; i++ {
		switch {
		case i >= 20 && i < 30: // alvo fora do ar
			ws = append(ws, win(i, -1, 0))
		case i >= 30 && i < 34: // voltando: lento
			ws = append(ws, win(i, 2000, 0.05))
		default:
			ws = append(ws, win(i, 100, 0))
		}
	}
	ev := []store.FaultEvent{{FaultID: "k", Type: scenario.FaultKillRestart, State: store.FaultReverted, AppliedAt: f(20), RevertedAt: f(30)}}
	v := Evaluate(sc, ws, ev)
	if len(v.Faults) != 1 {
		t.Fatalf("esperava 1 falha: %+v", v)
	}
	r := v.Faults[0]
	if r.During.Seconds != 10 || r.During.InSLO != 0 || r.StalledSeconds != 10 {
		t.Fatalf("durante a falha: %+v", r)
	}
	if !r.Recovered || r.RecoverySeconds == nil || *r.RecoverySeconds != 4 {
		t.Fatalf("recuperação: %+v", r)
	}
	// 0,7*0 + 0,3*1 (4 s <= 5 s) = 30; nota = 0,2*100 + 0,8*30 = 44
	if r.Score != 30 || v.Score != 44 || v.Grade != GradeFragile {
		t.Fatalf("nota: falha %d, total %d (%s)", r.Score, v.Score, v.Grade)
	}
	if v.Baseline.Seconds != 60-10-7 || v.Baseline.CompliancePct != 100 {
		t.Fatalf("linha de base: %+v", v.Baseline)
	}
}

func TestChaosOnlyUsesProbe(t *testing.T) {
	sc := scenario.Scenario{Mode: scenario.ModeChaos, Faults: []scenario.Fault{{ID: "l", Type: scenario.FaultLatency}}}
	var ws []load.Window
	for i := int64(0); i < 40; i++ {
		p := &load.Probe{Ms: 5, Status: 200, OK: true}
		if i >= 10 && i < 20 {
			p = &load.Probe{Ms: 800, Status: 200, OK: true} // acima do p95 de 500 ms
		}
		ws = append(ws, load.Window{T: i, Probe: p})
	}
	ev := []store.FaultEvent{{FaultID: "l", Type: scenario.FaultLatency, State: store.FaultReverted, AppliedAt: f(10), RevertedAt: f(20)}}
	v := Evaluate(sc, ws, ev)
	r := v.Faults[0]
	if r.During.InSLO != 0 || r.RecoverySeconds == nil || *r.RecoverySeconds != 0 || r.Score != 30 {
		t.Fatalf("falha: %+v", r)
	}
	if len(v.Checks) != 1 || !v.Checks[0].Pass {
		t.Fatalf("disponibilidade deveria passar (a sonda respondeu sempre): %+v", v.Checks)
	}
}

func TestNoRecoveryBeforeEnd(t *testing.T) {
	sc := scenario.Scenario{Mode: scenario.ModeBoth}
	var ws []load.Window
	for i := int64(0); i < 20; i++ {
		p95 := 100.0
		if i >= 10 {
			p95 = 900
		}
		ws = append(ws, win(i, p95, 0))
	}
	ev := []store.FaultEvent{{FaultID: "c", Type: scenario.FaultCPUStress, State: store.FaultReverted, AppliedAt: f(10), RevertedAt: f(15)}}
	v := Evaluate(sc, ws, ev)
	if v.Faults[0].Recovered || v.Faults[0].Score != 0 {
		t.Fatalf("não deveria ter recuperado: %+v", v.Faults[0])
	}
}
