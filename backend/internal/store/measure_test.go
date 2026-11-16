package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
)

func TestVersionsAndSummary(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Execução antiga (sem registro): nada, sem erro.
	if vs, err := s.Versions(ctx, "antiga"); err != nil || vs != nil {
		t.Fatalf("execução sem versões: %v %v", vs, err)
	}
	if sum, err := s.Summary(ctx, "antiga"); err != nil || sum != nil {
		t.Fatalf("execução sem resumo: %v %v", sum, err)
	}

	vs := []Version{
		{Role: RoleTarget, Name: "pix/pix-api", Commit: "d4c145f82ff2", Images: []Image{{Container: "pix-api", Image: "acr.io/pix-api:d4c145f82ff2", ID: "acr.io/pix-api@sha256:abc", Pods: 2}}},
		{Role: RoleDependency, Name: "pix/pix-postgres", Images: []Image{{Container: "postgres", Image: "postgres:16-alpine", ID: "sha256:def", Pods: 1}}},
	}
	if err := s.SaveVersions(ctx, "r1", vs); err != nil {
		t.Fatal(err)
	}
	got, err := s.Versions(ctx, "r1")
	if err != nil || !reflect.DeepEqual(got, vs) {
		t.Fatalf("versões: %+v %v", got, err)
	}
	if c := TargetCommit(got); c != "d4c145f82ff2" {
		t.Fatalf("commit do alvo = %q", c)
	}
	// Pods com versões diferentes: não dá para dizer que um commit só rodou.
	vs[0].Mixed = true
	if c := TargetCommit(vs); c != "" {
		t.Fatalf("alvo com versões misturadas não tem um commit: %q", c)
	}

	sum := load.Summary{OpenModel: true, TargetRate: 200, WarmupSeconds: 10,
		Steady:    load.Segment{Requests: 2000, RPS: 100, P95Ms: 48.25},
		Endpoints: []load.EndpointSummary{{Method: "GET", Path: "/items", Segment: load.Segment{Requests: 1500, P95Ms: 4.4}}}}
	if err := s.SaveSummary(ctx, "r1", sum); err != nil {
		t.Fatal(err)
	}
	back, err := s.Summary(ctx, "r1")
	if err != nil || back == nil || !reflect.DeepEqual(*back, sum) {
		t.Fatalf("resumo: %+v %v", back, err)
	}
}
