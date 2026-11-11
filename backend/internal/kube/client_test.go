package kube_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube/kubefake"
)

func TestParseQuantity(t *testing.T) {
	cases := map[string]float64{
		"250m": 0.25, "1": 1, "2.5": 2.5, "123456789n": 0.123456789, "500u": 0.0005,
		"64Mi": 64 << 20, "1Gi": 1 << 30, "512Ki": 512 << 10, "1G": 1e9, "3k": 3000, "": 0, "abc": 0,
	}
	for in, want := range cases {
		if got := kube.ParseQuantity(in); math.Abs(got-want) > 1e-12 {
			t.Errorf("ParseQuantity(%q) = %v, quer %v", in, got, want)
		}
	}
}

func TestSelector(t *testing.T) {
	if got := kube.Selector(map[string]string{"b": "2", "a": "1"}); got != "a=1,b=2" {
		t.Fatalf("Selector = %q", got)
	}
	if kube.Selector(nil) != "" {
		t.Fatal("selector vazio deveria ser vazio")
	}
}

func TestClientAgainstFake(t *testing.T) {
	f := kubefake.New()
	defer f.Close()
	c := kube.NewStatic(f.URL, "tok", "nac")
	ctx := context.Background()

	f.Deployment("shop", "api", map[string]string{"app": "api"}, 2)
	f.Metrics["api-0"] = [2]string{"250m", "64Mi"}
	f.Metrics["api-1"] = [2]string{"500000000n", "32Mi"}

	d, err := c.GetDeployment(ctx, "shop", "api")
	if err != nil || d.Status.ReadyReplicas != 2 || d.Spec.Selector.MatchLabels["app"] != "api" {
		t.Fatalf("GetDeployment = %+v, %v", d, err)
	}
	if _, err := c.GetDeployment(ctx, "shop", "nope"); !errors.Is(err, kube.ErrNotFound) {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
	pods, err := c.ListPods(ctx, "shop", "app=api")
	if err != nil || len(pods) != 2 {
		t.Fatalf("ListPods = %d, %v", len(pods), err)
	}
	if pods, _ := c.ListPods(ctx, "shop", "app=outro"); len(pods) != 0 {
		t.Fatalf("o seletor não filtrou: %d pods", len(pods))
	}
	usage, err := c.PodMetrics(ctx, "shop", "app=api")
	if err != nil || len(usage) != 2 {
		t.Fatalf("PodMetrics = %+v, %v", usage, err)
	}
	var cpu, mem float64
	for _, u := range usage {
		cpu += u.CPUCores
		mem += u.MemBytes
	}
	if math.Abs(cpu-0.75) > 1e-9 || mem != 96<<20 {
		t.Fatalf("soma cpu=%v mem=%v", cpu, mem)
	}
	f.Logs = "linha 1\nlinha 2\n"
	if s, err := c.PodLogs(ctx, "shop", "api-0", 10); err != nil || s != f.Logs {
		t.Fatalf("PodLogs = %q, %v", s, err)
	}
	// Delete de algo que não existe não é erro.
	if err := c.Delete(ctx, "/apis/batch/v1/namespaces/nac/jobs/x", "Background"); err != nil {
		t.Fatal(err)
	}
	// Conflito vira APIError.
	obj := map[string]any{"metadata": map[string]any{"name": "j"}}
	if err := c.Create(ctx, "/apis/batch/v1/namespaces/nac/jobs", obj); err != nil {
		t.Fatal(err)
	}
	var apiErr *kube.APIError
	if err := c.Create(ctx, "/apis/batch/v1/namespaces/nac/jobs", obj); !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("esperava 409, veio %v", err)
	}
}
