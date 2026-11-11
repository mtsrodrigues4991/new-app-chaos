package scenario

import (
	"strings"
	"testing"
)

func TestStagesSumToDuration(t *testing.T) {
	for _, typ := range []ProfileType{ProfileRamp, ProfilePlateau, ProfileSpike} {
		p := Profile{Type: typ, VUs: 50, DurationSeconds: 60}
		st, err := p.Stages()
		if err != nil {
			t.Fatal(err)
		}
		total, peak := 0, 0
		for _, s := range st {
			total += s.DurationSeconds
			if s.Target > peak {
				peak = s.Target
			}
		}
		if total != 60 {
			t.Errorf("%s: soma das durações = %d, esperado 60", typ, total)
		}
		if peak != 50 {
			t.Errorf("%s: pico = %d, esperado 50", typ, peak)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Scenario{
		Name:      "teste",
		Target:    Target{Container: "demo-api", BaseURL: "http://demo-api:8080"},
		Endpoints: []Endpoint{{Method: "GET", Path: "/items", Weight: 1}},
		Profile:   Profile{Type: ProfileRamp, VUs: 10, DurationSeconds: 30},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("esperava válido: %v", err)
	}
	bad := ok
	bad.Endpoints = nil
	bad.Profile.Type = "x"
	if err := bad.Validate(); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestValidateFaults(t *testing.T) {
	base := Scenario{
		Name:      "caos",
		Target:    Target{Container: "demo-api", BaseURL: "http://demo-api:8080"},
		Mode:      ModeChaos,
		ProbePath: "/health",
		Profile:   Profile{DurationSeconds: 60},
		Faults: []Fault{
			{ID: "a", Type: FaultLatency, StartSeconds: 5, DurationSeconds: 10, Params: map[string]string{ParamLatency: "300"}},
			{ID: "b", Type: FaultMemStress, StartSeconds: 20, DurationSeconds: 10, Params: map[string]string{ParamMemBytes: "50%"}},
			{ID: "c", Type: FaultKillRestart, StartSeconds: 40, DurationSeconds: 5},
		},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("modo só caos sem endpoints/VUs deveria ser válido: %v", err)
	}
	cases := map[string]func(s *Scenario){
		"sobreposição":      func(s *Scenario) { s.Faults[1].StartSeconds = 10 },
		"passa do fim":      func(s *Scenario) { s.Faults[2].DurationSeconds = 30 },
		"parâmetro ruim":    func(s *Scenario) { s.Faults[0].Params[ParamLatency] = "abc" },
		"memória ruim":      func(s *Scenario) { s.Faults[1].Params[ParamMemBytes] = "150%" },
		"sem sonda":         func(s *Scenario) { s.ProbePath = "" },
		"sem falhas":        func(s *Scenario) { s.Faults = nil },
		"tipo desconhecido": func(s *Scenario) { s.Faults[0].Type = "x" },
	}
	for name, mut := range cases {
		s := base
		s.Faults = nil
		for _, f := range base.Faults {
			p := map[string]string{}
			for k, v := range f.Params {
				p[k] = v
			}
			f.Params = p
			s.Faults = append(s.Faults, f)
		}
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: esperava erro", name)
		}
	}
	// No modo só carga, as falhas não são validadas (ficam guardadas, mas não rodam).
	load := base
	load.Mode = ModeLoad
	load.Endpoints = []Endpoint{{Method: "GET", Path: "/", Weight: 1}}
	load.Profile = Profile{Type: ProfileRamp, VUs: 5, DurationSeconds: 30}
	if err := load.Validate(); err != nil {
		t.Fatalf("modo carga: %v", err)
	}
}

func TestValidateKubernetesTarget(t *testing.T) {
	sc := Scenario{
		Name:      "k8s",
		Target:    Target{Platform: PlatformKubernetes, Namespace: "demo", Deployment: "demo-api", BaseURL: "http://demo-api.demo:8080"},
		Mode:      ModeChaos,
		ProbePath: "/health",
		Profile:   Profile{DurationSeconds: 60},
		Faults:    []Fault{{ID: "a", Type: FaultKillRestart, StartSeconds: 5, DurationSeconds: 10, Params: map[string]string{ParamPods: PodsAll}}},
	}
	if err := sc.Validate(); err != nil {
		t.Fatalf("esperava válido: %v", err)
	}
	if sc.Target.Name() != "demo/demo-api" || sc.Faults[0].PodsMode() != PodsAll {
		t.Fatalf("Name=%q PodsMode=%q", sc.Target.Name(), sc.Faults[0].PodsMode())
	}
	if (Fault{Type: FaultKillRestart}).PodsMode() != PodsOne || (Fault{Type: FaultLatency}).PodsMode() != PodsAll {
		t.Fatal("padrões de pods errados")
	}
	noDep := sc
	noDep.Target.Deployment = ""
	if noDep.Validate() == nil {
		t.Fatal("sem Deployment deveria falhar")
	}
	docker := sc
	docker.Target = Target{Container: "demo-api", BaseURL: "http://demo-api:8080"}
	if err := docker.Validate(); err == nil || !strings.Contains(err.Error(), "pods") {
		t.Fatalf("parâmetro pods fora do Kubernetes deveria falhar, veio %v", err)
	}
	bad := sc
	bad.Faults = []Fault{{ID: "a", Type: FaultLatency, StartSeconds: 5, DurationSeconds: 10, Params: map[string]string{ParamPods: "metade"}}}
	if bad.Validate() == nil {
		t.Fatal("pods inválido deveria falhar")
	}
}
