package chaosmesh

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

var tgt = Target{Namespace: "shop", MatchLabels: map[string]string{"app": "api"}}

func build(t *testing.T, f scenario.Fault) (Kind, map[string]any) {
	t.Helper()
	k, obj, err := Build(f, tgt, "n1", "run1")
	if err != nil {
		t.Fatal(err)
	}
	return k, obj["spec"].(map[string]any)
}

func TestBuildLatency(t *testing.T) {
	k, spec := build(t, scenario.Fault{Type: scenario.FaultLatency, StartSeconds: 5, DurationSeconds: 20, Params: map[string]string{"latency": "300", "jitter": "50"}})
	if k != NetworkChaos || spec["action"] != "delay" || spec["direction"] != "to" || spec["mode"] != "all" {
		t.Fatalf("kind=%v spec=%v", k, spec)
	}
	d := spec["delay"].(map[string]any)
	if d["latency"] != "300ms" || d["jitter"] != "50ms" || spec["duration"] != "50s" {
		t.Fatalf("delay=%v duration=%v", d, spec["duration"])
	}
	sel := spec["selector"].(map[string]any)
	if sel["namespaces"].([]string)[0] != "shop" || sel["labelSelectors"].(map[string]string)["app"] != "api" {
		t.Fatalf("selector=%v", sel)
	}
}

func TestBuildLossDefaults(t *testing.T) {
	k, spec := build(t, scenario.Fault{Type: scenario.FaultPacketLoss, DurationSeconds: 10})
	if k != NetworkChaos || spec["action"] != "loss" {
		t.Fatal(spec)
	}
	if spec["loss"].(map[string]any)["loss"] != scenario.FaultDefaults[scenario.FaultPacketLoss][scenario.ParamLoss] {
		t.Fatalf("loss=%v", spec["loss"])
	}
}

func TestBuildStress(t *testing.T) {
	k, spec := build(t, scenario.Fault{Type: scenario.FaultCPUStress, DurationSeconds: 10, Params: map[string]string{"cpu-workers": "2", "cpu-load-percentage": "80", "pods": "one"}})
	cpu := spec["stressors"].(map[string]any)["cpu"].(map[string]any)
	if k != StressChaos || cpu["workers"] != 2 || cpu["load"] != 80 || spec["mode"] != "one" {
		t.Fatalf("kind=%v spec=%v", k, spec)
	}
	_, spec = build(t, scenario.Fault{Type: scenario.FaultMemStress, DurationSeconds: 10, Params: map[string]string{"memory-vm-bytes": "256m"}})
	if mem := spec["stressors"].(map[string]any)["memory"].(map[string]any); mem["size"] != "256MB" {
		t.Fatalf("memory=%v", mem)
	}
}

func TestBuildKill(t *testing.T) {
	k, spec := build(t, scenario.Fault{Type: scenario.FaultKillRestart, DurationSeconds: 30})
	if k != PodChaos || spec["action"] != "pod-kill" || spec["mode"] != "one" || spec["duration"] != nil {
		t.Fatalf("kind=%v spec=%v", k, spec)
	}
	_, spec = build(t, scenario.Fault{Type: scenario.FaultKillRestart, DurationSeconds: 30, Params: map[string]string{"pods": "all"}})
	if spec["mode"] != "all" {
		t.Fatal(spec)
	}
}

func TestBuildInvalid(t *testing.T) {
	if _, _, err := Build(scenario.Fault{Type: scenario.FaultLatency, Params: map[string]string{"pods": "metade"}}, tgt, "n", "r"); err == nil {
		t.Fatal("esperava erro de pods inválido")
	}
	if _, _, err := Build(scenario.Fault{Type: "xyz"}, tgt, "n", "r"); err == nil {
		t.Fatal("esperava erro de tipo desconhecido")
	}
}

func TestMemSizeAndName(t *testing.T) {
	for in, want := range map[string]string{"256m": "256MB", "1g": "1GB", "512k": "512KB", "80%": "80%", "1024": "1024", "2G": "2GB"} {
		if got := MemSize(in); got != want {
			t.Errorf("MemSize(%q)=%q, quer %q", in, got, want)
		}
	}
	if got := Name("nac", "Run_ABC", "latency", "5"); got != "nac-run-abc-latency-5" {
		t.Fatalf("Name=%q", got)
	}
	if got := Name(strings.Repeat("a", 70), "x"); len(got) > 63 || strings.HasSuffix(got, "-") {
		t.Fatalf("Name longo=%q", got)
	}
}

func TestInjected(t *testing.T) {
	parse := func(s string) Status {
		var st Status
		if err := json.Unmarshal([]byte(s), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if ok, _ := parse(`{}`).Injected(); ok {
		t.Fatal("status vazio não está injetado")
	}
	if ok, _ := parse(`{"conditions":[{"type":"AllInjected","status":"True"}]}`).Injected(); !ok {
		t.Fatal("AllInjected=True deveria valer")
	}
	if ok, _ := parse(`{"experiment":{"containerRecords":[{"phase":"Injected"},{"phase":"Injected"}]}}`).Injected(); !ok {
		t.Fatal("todos Injected deveria valer")
	}
	ok, msg := parse(`{"conditions":[{"type":"AllInjected","status":"False"}],"experiment":{"containerRecords":[{"phase":"Injected"},{"phase":"Not Injected","events":[{"type":"Failed","message":"tc: não achou a interface"}]}]}}`).Injected()
	if ok || msg != "tc: não achou a interface" {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}
