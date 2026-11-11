package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube/kubefake"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

func TestParseLogLines(t *testing.T) {
	started := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	ls := parseLogLines("2026-11-11T20:00:12.345678901Z ERROR conexão recusada\r\nsem timestamp\n\n2026-11-11T19:59:59Z antes\n", started)
	if len(ls) != 3 || ls[0].T != 12.3 || ls[0].Text != "ERROR conexão recusada" || ls[1].T != -1 || ls[2].T != -1 {
		t.Fatalf("linhas=%+v", ls)
	}
}

func TestK8sDiagnostics(t *testing.T) {
	f, p := cluster(t)
	f.Deployment("pix", "pix-postgres", map[string]string{"app": "pix-postgres"}, 1)
	started := time.Now().UTC().Truncate(time.Second)
	f.Logs = started.Add(5*time.Second).Format(time.RFC3339Nano) + " FATAL: terminating connection\n"
	sc := k8sScenario("shop", "api")
	sc.Faults = []scenario.Fault{{Type: scenario.FaultKillRestart, Dependency: &scenario.Dependency{Namespace: "pix", Deployment: "pix-postgres"}}}
	tg, err := p.Resolve(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	c := tg.(Collector)
	c.Snapshot(context.Background(), started)
	// O kill apaga o pod do banco: o log dele tem que continuar na coleta final, marcado.
	f.Remove(kubefake.Ref{GV: "v1", Plural: "pods"}, "pix", "pix-postgres-0")
	ev := func(name, typ, reason string, at time.Time) kubefake.Obj {
		return kubefake.Obj{"metadata": kubefake.Obj{"name": name, "namespace": "pix"}, "type": typ, "reason": reason,
			"message": "msg " + reason, "count": 2, "involvedObject": kubefake.Obj{"kind": "Pod", "name": "pix-postgres-0"},
			"lastTimestamp": at.Format(time.RFC3339)}
	}
	f.Put(kubefake.Ref{GV: "v1", Plural: "events"}, ev("velho", "Normal", "Pulled", started.Add(-time.Hour)))
	f.Put(kubefake.Ref{GV: "v1", Plural: "events"}, ev("e1", "Warning", "BackOff", started.Add(20*time.Second)))
	// Do Chaos Mesh fica só o que importa (Applied/Recovered), não a contabilidade interna.
	chaosEv := func(name, reason string) kubefake.Obj {
		o := ev(name, "Normal", reason, started.Add(25*time.Second))
		o["involvedObject"] = kubefake.Obj{"kind": "PodChaos", "name": "nac-r1"}
		return o
	}
	f.Put(kubefake.Ref{GV: "v1", Plural: "events"}, chaosEv("c1", "Updated"))
	f.Put(kubefake.Ref{GV: "v1", Plural: "events"}, chaosEv("c2", "FinalizerInited"))
	f.Put(kubefake.Ref{GV: "v1", Plural: "events"}, chaosEv("c3", "Applied"))
	d := c.Diagnostics(context.Background(), started)

	if len(d.Logs) != 3 { // 2 pods do alvo + o do banco
		t.Fatalf("logs=%+v", d.Logs)
	}
	if d.Logs[0].Role != "alvo" || d.Logs[2].Role != "dependência pix/pix-postgres" || !d.Logs[2].Gone || d.Logs[0].Gone {
		t.Fatalf("ordem/flags: %+v", d.Logs)
	}
	if l := d.Logs[2].Lines; len(l) != 1 || l[0].T != 5 || !strings.Contains(l[0].Text, "FATAL") {
		t.Fatalf("linhas=%+v", l)
	}
	if len(d.Events) != 2 || d.Events[0].Reason != "BackOff" || d.Events[0].T != 20 || d.Events[0].Object != "Pod/pix-postgres-0" || d.Events[0].Count != 2 ||
		d.Events[1].Reason != "Applied" {
		t.Fatalf("eventos=%+v", d.Events)
	}
}
