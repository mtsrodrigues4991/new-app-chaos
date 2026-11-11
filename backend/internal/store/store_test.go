package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sc := scenario.Scenario{ID: "s1", Name: "cenário", Profile: scenario.Profile{Type: scenario.ProfileRamp, VUs: 5, DurationSeconds: 10}}
	if err := s.SaveScenario(ctx, sc); err != nil {
		t.Fatal(err)
	}
	sc.Name = "renomeado"
	if err := s.SaveScenario(ctx, sc); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetScenario(ctx, "s1")
	if err != nil || got.Name != "renomeado" {
		t.Fatalf("GetScenario: %v %+v", err, got)
	}

	now := time.Now()
	if err := s.CreateRun(ctx, Run{ID: "r1", ScenarioID: "s1", Scenario: sc, Status: RunPending, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunStatus(ctx, "r1", RunRunning, "", &now, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWindows(ctx, "r1", []load.Window{{T: 1, RPS: 10}, {T: 0, RPS: 5}}); err != nil {
		t.Fatal(err)
	}
	ws, err := s.Windows(ctx, "r1")
	if err != nil || len(ws) != 2 || ws[0].T != 0 || ws[1].RPS != 10 {
		t.Fatalf("Windows: %v %+v", err, ws)
	}

	n, err := s.FailInterrupted(ctx)
	if err != nil || n != 1 {
		t.Fatalf("FailInterrupted: %v %d", err, n)
	}
	r, err := s.GetRun(ctx, "r1")
	if err != nil || r.Status != RunFailed || r.StartedAt == nil || r.EndedAt == nil || r.ScenarioName != "renomeado" {
		t.Fatalf("GetRun: %v %+v", err, r)
	}
	runs, err := s.ListRuns(ctx, "s1", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: %v %d", err, len(runs))
	}
}
