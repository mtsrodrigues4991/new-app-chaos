package scenario

import "testing"

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
