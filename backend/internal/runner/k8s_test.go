package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/chaosmesh"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube/kubefake"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

type obj = kubefake.Obj

// cluster sobe a API falsa com um Deployment "shop/api" de 2 réplicas.
func cluster(t *testing.T) (*kubefake.Server, *k8sPlatform) {
	t.Helper()
	f := kubefake.New()
	t.Cleanup(f.Close)
	f.Deployment("shop", "api", map[string]string{"app": "api"}, 2)
	p := newK8sPlatform(kube.NewStatic(f.URL, "", "nac"))
	p.InjectTimeout = 300 * time.Millisecond
	p.Poll = 20 * time.Millisecond
	return f, p
}

func k8sScenario(ns, name string) scenario.Scenario {
	return scenario.Scenario{Target: scenario.Target{Platform: scenario.PlatformKubernetes, Namespace: ns, Deployment: name, BaseURL: "http://api.shop:8080/"}, ProbePath: "/health"}
}

// injectAll faz o Chaos Mesh falso marcar toda falha criada como injetada.
func injectAll(s *kubefake.Server, r kubefake.Ref, o obj) {
	if strings.HasPrefix(r.GV, "chaos-mesh.org") {
		o["status"] = obj{"conditions": []obj{{"type": "AllInjected", "status": "True"}}}
	}
}

func TestK8sResolve(t *testing.T) {
	f, p := cluster(t)
	ctx := context.Background()
	if _, err := p.Resolve(ctx, k8sScenario("shop", "nope")); err == nil || !strings.Contains(err.Error(), "não existe") {
		t.Fatalf("esperava 'não existe', veio %v", err)
	}
	f.Update(kubefake.Deployments, "shop", "api", func(o obj) { o["status"].(obj)["readyReplicas"] = 0 })
	if _, err := p.Resolve(ctx, k8sScenario("shop", "api")); err == nil || !strings.Contains(err.Error(), "nenhum pod pronto") {
		t.Fatalf("esperava 'nenhum pod pronto', veio %v", err)
	}
	f.Update(kubefake.Deployments, "shop", "api", func(o obj) { o["status"].(obj)["readyReplicas"] = 2 })
	tg, err := p.Resolve(ctx, k8sScenario("shop", "api"))
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := tg.ProbeURL(ctx, k8sScenario("shop", "api")); u != "http://api.shop:8080/health" {
		t.Fatalf("ProbeURL=%q", u)
	}
}

func TestK8sApplyFaultAndRevert(t *testing.T) {
	f, p := cluster(t)
	f.OnCreate = injectAll
	ctx := context.Background()
	tg, err := p.Resolve(ctx, k8sScenario("shop", "api"))
	if err != nil {
		t.Fatal(err)
	}
	revert, err := tg.ApplyFault(ctx, "r1", scenario.Fault{Type: scenario.FaultLatency, StartSeconds: 10, DurationSeconds: 20})
	if err != nil {
		t.Fatal(err)
	}
	ref := kubefake.Ref{GV: chaosmesh.Group, Plural: "networkchaos"}
	got := f.List(ref)
	if len(got) != 1 {
		t.Fatalf("esperava 1 NetworkChaos, há %d", len(got))
	}
	if l := kubefake.Labels(got[0]); l[chaosmesh.LabelManaged] != chaosmesh.ManagedBy || l[chaosmesh.LabelRun] != "r1" {
		t.Fatalf("labels=%v", l)
	}
	if err := revert(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(f.List(ref)); n != 0 {
		t.Fatalf("o revert não apagou a falha (%d restantes)", n)
	}
}

func TestK8sApplyFaultNotConfirmed(t *testing.T) {
	f, p := cluster(t)
	f.OnCreate = func(s *kubefake.Server, r kubefake.Ref, o obj) {
		o["status"] = obj{"experiment": obj{"containerRecords": []obj{{"phase": "Not Injected", "events": []obj{{"type": "Failed", "message": "sem permissão"}}}}}}
	}
	tg, _ := p.Resolve(context.Background(), k8sScenario("shop", "api"))
	_, err := tg.ApplyFault(context.Background(), "r1", scenario.Fault{Type: scenario.FaultCPUStress, DurationSeconds: 5})
	if err == nil || !strings.Contains(err.Error(), "não confirmou") || !strings.Contains(err.Error(), "sem permissão") {
		t.Fatalf("erro=%v", err)
	}
	if n := len(f.List(kubefake.Ref{GV: chaosmesh.Group, Plural: "stresschaos"})); n != 0 {
		t.Fatalf("a falha não confirmada deveria ter sido apagada (%d restantes)", n)
	}
}

func TestK8sApplyFaultWithoutChaosMesh(t *testing.T) {
	f, p := cluster(t)
	delete(f.Groups, chaosmesh.Group)
	tg, _ := p.Resolve(context.Background(), k8sScenario("shop", "api"))
	_, err := tg.ApplyFault(context.Background(), "r1", scenario.Fault{Type: scenario.FaultKillRestart, DurationSeconds: 5})
	if err == nil || !strings.Contains(err.Error(), "Chaos Mesh não está instalado") {
		t.Fatalf("erro=%v", err)
	}
}

// jobPods faz a API falsa criar o pod de cada Job criado (com o estado dado).
func jobPods(phase, waiting string) func(*kubefake.Server, kubefake.Ref, obj) {
	return func(s *kubefake.Server, r kubefake.Ref, o obj) {
		if r != kubefake.Jobs {
			return
		}
		m := o["metadata"].(map[string]any)
		labels := obj{"job-name": m["name"]}
		tpl := o["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["labels"].(map[string]any)
		for k, v := range tpl {
			labels[k] = v
		}
		state := obj{"running": obj{}}
		if waiting != "" {
			state = obj{"waiting": obj{"reason": waiting, "message": "imagem não encontrada"}}
		}
		s.Put(kubefake.Pods, obj{
			"metadata": obj{"name": fmt.Sprint(m["name"], "-abc"), "namespace": m["namespace"], "labels": labels},
			"status":   obj{"phase": phase, "podIP": "10.1.2.3", "containerStatuses": []obj{{"name": "k6", "state": state}}},
		})
	}
}

func TestK8sK6Lifecycle(t *testing.T) {
	f, p := cluster(t)
	f.OnCreate = jobPods("Running", "")
	ctx := context.Background()
	tg, _ := p.Resolve(ctx, k8sScenario("shop", "api"))
	k, err := tg.StartK6(ctx, "r1", "grafana/k6:latest", "export default function(){}")
	if err != nil {
		t.Fatal(err)
	}
	job := f.Get(kubefake.Jobs, "nac", "nac-k6-r1")
	if job == nil {
		t.Fatalf("Job não criado; pedidos: %v", f.Requests)
	}
	ct := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if ct["image"] != "grafana/k6:latest" || !strings.Contains(fmt.Sprint(ct["env"]), "K6_SCRIPT") {
		t.Fatalf("container=%v", ct)
	}
	if u, err := k.DashboardURL(ctx); err != nil || u != "http://10.1.2.3:5665/events" {
		t.Fatalf("DashboardURL=%q, %v", u, err)
	}

	// Terminou com código 0.
	f.Update(kubefake.Pods, "nac", "nac-k6-r1-abc", func(o obj) {
		o["status"].(obj)["containerStatuses"] = []obj{{"name": "k6", "state": obj{"terminated": obj{"exitCode": 0}}}}
	})
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if code, err := k.Wait(wctx); err != nil || code != 0 {
		t.Fatalf("Wait=%d, %v", code, err)
	}
	if err := k.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Get(kubefake.Jobs, "nac", "nac-k6-r1") != nil || f.Get(kubefake.Pods, "nac", "nac-k6-r1-abc") != nil {
		t.Fatal("Remove deveria apagar o Job e o pod")
	}
}

func TestK8sK6KillMeansAborted(t *testing.T) {
	f, p := cluster(t)
	f.OnCreate = jobPods("Running", "")
	ctx := context.Background()
	tg, _ := p.Resolve(ctx, k8sScenario("shop", "api"))
	k, _ := tg.StartK6(ctx, "r2", "k6", "")
	done := make(chan int, 1)
	go func() { c, _ := k.Wait(ctx); done <- c }()
	time.Sleep(60 * time.Millisecond)
	if err := k.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-done:
		if c != 137 {
			t.Fatalf("código=%d, quer 137", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait não voltou depois do Kill")
	}
}

func TestK8sK6StuckImage(t *testing.T) {
	f, p := cluster(t)
	f.OnCreate = jobPods("Pending", "ImagePullBackOff")
	ctx := context.Background()
	tg, _ := p.Resolve(ctx, k8sScenario("shop", "api"))
	k, _ := tg.StartK6(ctx, "r3", "k6:nao-existe", "")
	if _, err := k.DashboardURL(ctx); err == nil || !strings.Contains(err.Error(), "ImagePullBackOff") {
		t.Fatalf("erro=%v", err)
	}
	if _, err := k.Wait(ctx); err == nil {
		t.Fatal("Wait deveria falhar com a imagem travada")
	}
}

func TestK8sStats(t *testing.T) {
	f, p := cluster(t)
	f.Pod("shop", "api-2", map[string]string{"app": "api"}, false) // subindo, não pronto
	f.Pod("shop", "outro", map[string]string{"app": "outro"}, true)
	f.Metrics["api-0"] = [2]string{"250m", "64Mi"}
	f.Metrics["api-1"] = [2]string{"750m", "128Mi"}
	ctx, cancel := context.WithCancel(context.Background())
	tg, _ := p.Resolve(ctx, k8sScenario("shop", "api"))
	var got Sample
	_ = tg.Stats(ctx, func(s Sample) { got = s; cancel() })
	if got.Total != 3 || got.Ready != 2 || got.NoUsage || got.CPU != 100 || got.MemMB != 192 {
		t.Fatalf("amostra=%+v", got)
	}

	// Sem metrics-server: ainda conta os pods.
	delete(f.Groups, "metrics.k8s.io/v1beta1")
	ctx, cancel = context.WithCancel(context.Background())
	_ = tg.Stats(ctx, func(s Sample) { got = s; cancel() })
	if !got.NoUsage || got.Total != 3 {
		t.Fatalf("amostra sem métricas=%+v", got)
	}
}

func TestK8sRecover(t *testing.T) {
	f, p := cluster(t)
	managed := obj{chaosmesh.LabelManaged: chaosmesh.ManagedBy}
	nc := kubefake.Ref{GV: chaosmesh.Group, Plural: "networkchaos"}
	f.Put(nc, obj{"metadata": obj{"name": "velha", "namespace": "shop", "labels": managed}})
	f.Put(nc, obj{"metadata": obj{"name": "de-outro", "namespace": "shop"}})
	f.Put(kubefake.Jobs, obj{"metadata": obj{"name": "nac-k6-x", "namespace": "nac", "labels": obj{labelRole: RoleK6}}})
	f.Put(kubefake.Pods, obj{"metadata": obj{"name": "nac-k6-x-1", "namespace": "nac", "labels": obj{"job-name": "nac-k6-x"}}})
	p.Recover(context.Background(), nil)
	if f.Get(nc, "shop", "velha") != nil || f.Get(nc, "shop", "de-outro") == nil {
		t.Fatal("o Recover deveria apagar só as falhas do app")
	}
	if f.Get(kubefake.Jobs, "nac", "nac-k6-x") != nil || f.Get(kubefake.Pods, "nac", "nac-k6-x-1") != nil {
		t.Fatal("o Recover deveria apagar os Jobs do k6 e seus pods")
	}
}
