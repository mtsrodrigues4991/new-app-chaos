package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube/kubefake"
)

func job() Job {
	return Job{ID: "abc123-1", Repo: "dono/repo", Commit: "0123456789abcdef0123456789abcdef01234567", Patch: "--- a/x\n+++ b/x\n", PatchDir: "app",
		Task: "aplique a correção", Model: "claude-opus-5-5", TestImage: "maven:3.9-eclipse-temurin-17", TestCommand: "mvn -B -q verify", WorkDir: "app"}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func report(applied, exit, diff, agent string) string {
	return "2026-11-16T22:00:00Z ###NAC applied=" + applied + "\n2026-11-16T22:00:00Z ###NAC test_exit=" + exit +
		"\n2026-11-16T22:00:00Z ###NAC diff=" + b64(diff) + "\n2026-11-16T22:00:00Z ###NAC agent=" + b64(agent) +
		"\n2026-11-16T22:00:00Z ###NAC test_log=" + b64("BUILD SUCCESS") + "\n2026-11-16T22:00:00Z ###NAC done=1\n"
}

func TestParse(t *testing.T) {
	r, err := parse(report("1", "0", "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", ""))
	if err != nil || !r.Applied || r.TestExit != 0 || r.AgentRan || !strings.Contains(r.Diff, "+b") || r.TestLog != "BUILD SUCCESS" {
		t.Fatalf("patch que aplicou e passou: %+v %v", r, err)
	}
	agent := `{"type":"result","subtype":"success","is_error":false,"result":"Ajustei o pool.","total_cost_usd":0.42,"num_turns":7}`
	r, err = parse(report("0", "1", "diff", agent))
	if err != nil || r.Applied || r.TestExit != 1 || !r.AgentRan || r.AgentOutput != "Ajustei o pool." || r.CostUSD != 0.42 || r.Turns != 7 {
		t.Fatalf("rodada com agente e teste falhando: %+v %v", r, err)
	}
	// Pod que terminou sem relatar (ex.: log cortado) não vira resultado vazio válido.
	if _, err := parse("###NAC applied=1\n"); err == nil {
		t.Fatal("sem done=1 tem que dar erro")
	}
}

// O isolamento do pod é parte do contrato: sem conta de serviço, sem root, e cada passo só com
// as credenciais de que precisa.
func TestPodIsolation(t *testing.T) {
	k := &Kube{Namespace: "sandbox", Image: "agent:dev", AnthropicKey: "sk-ant", ReadToken: "ghp-leitura"}
	secret, jobObj, name := k.objects(job())
	if name != "nac-fix-abc123-1" || secret["metadata"].(map[string]any)["namespace"] != "sandbox" {
		t.Fatalf("nome/namespace: %s %+v", name, secret["metadata"])
	}
	raw, _ := json.Marshal(jobObj)
	var j struct {
		Spec struct {
			BackoffLimit          int
			ActiveDeadlineSeconds int
			Template              struct {
				Spec struct {
					AutomountServiceAccountToken *bool
					RestartPolicy                string
					InitContainers, Containers   []struct {
						Name, Image     string
						Command         []string
						Env             []struct{ Name, Value string }
						VolumeMounts    []struct{ Name, MountPath string }
						SecurityContext struct {
							RunAsNonRoot, AllowPrivilegeEscalation *bool
							Capabilities                           struct{ Drop []string }
						}
					}
					Volumes []struct {
						Name   string
						Secret *struct{ Items []struct{ Key string } }
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	spec := j.Spec.Template.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken || spec.RestartPolicy != "Never" || j.Spec.BackoffLimit != 0 || j.Spec.ActiveDeadlineSeconds != 1800 {
		t.Fatalf("pod: %+v", j.Spec)
	}
	keys := map[string][]string{}
	for _, v := range spec.Volumes {
		if v.Secret != nil {
			for _, it := range v.Secret.Items {
				keys[v.Name] = append(keys[v.Name], it.Key)
			}
		}
	}
	// A chave da Anthropic só no volume do agente; o token de leitura só no do checkout.
	if strings.Join(keys["in-agent"], ",") != "task.md,ANTHROPIC_API_KEY" || strings.Join(keys["in-checkout"], ",") != "patch.diff,GITHUB_TOKEN" {
		t.Fatalf("chaves por volume: %+v", keys)
	}
	steps := append(spec.InitContainers, spec.Containers...)
	if len(steps) != 4 {
		t.Fatalf("esperava 4 passos: %d", len(steps))
	}
	for _, c := range steps {
		sc := c.SecurityContext
		if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || len(sc.Capabilities.Drop) != 1 {
			t.Errorf("%s: contexto de segurança frouxo: %+v", c.Name, sc)
		}
		var in string
		for _, m := range c.VolumeMounts {
			if m.MountPath == "/in" {
				in = m.Name
			}
		}
		want := map[string]string{"checkout": "in-checkout", "agent": "in-agent", "verify": "", "report": ""}[c.Name]
		if in != want {
			t.Errorf("%s monta %q em /in, esperado %q", c.Name, in, want)
		}
		for _, e := range c.Env {
			if strings.Contains(e.Value, "sk-ant") || strings.Contains(e.Value, "ghp-") {
				t.Errorf("%s: credencial em variável de ambiente", c.Name)
			}
		}
	}
	// O passo que executa o build do repositório usa a imagem do cenário e não vê credencial nenhuma.
	verify := spec.InitContainers[2]
	if verify.Name != "verify" || verify.Image != "maven:3.9-eclipse-temurin-17" || !strings.Contains(verify.Command[2], "mvn -B -q verify") {
		t.Fatalf("verify: %+v", verify)
	}
}

func TestRunReadsResultAndCleansUp(t *testing.T) {
	f := kubefake.New()
	t.Cleanup(f.Close)
	f.Logs = report("1", "0", "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", "")
	f.OnCreate = func(s *kubefake.Server, r kubefake.Ref, o kubefake.Obj) {
		if r.Plural != "jobs" {
			return
		}
		name := o["metadata"].(map[string]any)["name"].(string)
		go s.Put(kubefake.Pods, kubefake.Obj{
			"metadata": kubefake.Obj{"name": name + "-x", "namespace": "sandbox", "labels": kubefake.Obj{"job-name": name}},
			"status":   kubefake.Obj{"phase": "Succeeded"},
		})
	}
	k := &Kube{Client: kube.NewStatic(f.URL, "", "nac"), Namespace: "sandbox", Image: "agent:dev", AnthropicKey: "sk-ant", Poll: 10 * time.Millisecond}
	res, err := k.Run(context.Background(), job())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.TestExit != 0 || !strings.Contains(res.Diff, "+b") {
		t.Fatalf("resultado: %+v", res)
	}
	// Nada da rodada fica no cluster: nem o Job, nem o Secret com a chave.
	if n := len(f.List(kubefake.Jobs)); n != 0 {
		t.Fatalf("sobrou Job: %d", n)
	}
	if n := len(f.List(kubefake.Ref{GV: "v1", Plural: "secrets"})); n != 0 {
		t.Fatalf("sobrou Secret: %d", n)
	}
}

func TestRunRefusals(t *testing.T) {
	if why := (&Kube{}).Ready(); !strings.Contains(why, "Kubernetes") {
		t.Fatalf("sem cluster: %q", why)
	}
	k := &Kube{Client: kube.NewStatic("http://127.0.0.1:1", "", "nac"), Namespace: "sandbox", Image: "agent:dev", AnthropicKey: "k"}
	noTest := job()
	noTest.TestCommand = ""
	if _, err := k.Run(context.Background(), noTest); err == nil || !strings.Contains(err.Error(), "construir e testar") {
		t.Fatalf("sem comando de teste: %v", err)
	}
}
