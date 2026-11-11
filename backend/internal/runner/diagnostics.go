package runner

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// Collector é opcional: um alvo que junta logs (e, no Kubernetes, eventos) para a IA.
type Collector interface {
	// Snapshot guarda os logs até agora. Roda antes de cada falha e de tempos em tempos,
	// porque um kill apaga o pod e, com ele, o log.
	Snapshot(ctx context.Context, started time.Time)
	// Diagnostics faz a última coleta e devolve tudo, no fim da execução.
	Diagnostics(ctx context.Context, started time.Time) store.Diagnostics
}

const (
	// LogTailLines limita as linhas guardadas por container (as mais recentes).
	LogTailLines = 3000
	// MaxEvents limita os eventos guardados (os Warning têm prioridade).
	MaxEvents = 300
	// SnapshotEvery é o intervalo das coletas de log durante a execução.
	SnapshotEvery = 15 * time.Second
)

// parseLogLines lê linhas no formato "<RFC3339Nano> texto" (timestamps do Kubernetes e do
// Docker) e devolve cada uma com t = segundos desde started.
func parseLogLines(raw string, started time.Time) []store.LogLine {
	out := []store.LogLine{}
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		ts, text, ok := strings.Cut(line, " ")
		at, err := time.Parse(time.RFC3339Nano, ts)
		if !ok || err != nil {
			out = append(out, store.LogLine{T: -1, Text: line})
			continue
		}
		t := math.Round(at.Sub(started).Seconds()*10) / 10
		out = append(out, store.LogLine{T: t, Text: text})
	}
	return out
}

// --- Kubernetes ---

type podGroup struct {
	ns, selector, role string
}

type k8sCollector struct {
	mu       sync.Mutex
	logs     map[string]*store.PodLog // ns/pod/container[/previous]
	groups   []podGroup
	resolved bool
	notes    []string
}

// groupsOf resolve (uma vez) os pods a acompanhar: o alvo e as dependências das falhas.
func (t *k8sTarget) groupsOf(ctx context.Context) []podGroup {
	c := &t.diag
	if c.resolved {
		return c.groups
	}
	c.resolved = true
	c.groups = []podGroup{{ns: t.ns, selector: t.selector, role: "alvo"}}
	seen := map[string]bool{}
	for _, d := range t.deps {
		if seen[d.Name()] || (d.Namespace == t.ns && d.Deployment == t.name) {
			continue
		}
		seen[d.Name()] = true
		dep, err := t.p.dependency(ctx, d)
		if err != nil {
			c.notes = append(c.notes, fmt.Sprintf("logs da dependência %s: %v", d.Name(), err))
			continue
		}
		c.groups = append(c.groups, podGroup{ns: d.Namespace, selector: kube.Selector(dep.MatchLabels), role: "dependência " + d.Name()})
	}
	return c.groups
}

func (t *k8sTarget) Snapshot(ctx context.Context, started time.Time) {
	c := &t.diag
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logs == nil {
		c.logs = map[string]*store.PodLog{}
	}
	alive := map[string]bool{}
	for _, g := range t.groupsOf(ctx) {
		pods, err := t.p.kube.ListPods(ctx, g.ns, g.selector)
		if err != nil {
			continue // tenta de novo na próxima coleta
		}
		for _, p := range pods {
			alive[g.ns+"/"+p.Metadata.Name] = true
			for _, cs := range p.Status.ContainerStatuses {
				t.fetch(ctx, c, g, p.Metadata.Name, cs.Name, started, false)
				if cs.RestartCount > 0 {
					t.fetch(ctx, c, g, p.Metadata.Name, cs.Name, started, true)
				}
			}
		}
	}
	for _, l := range c.logs {
		if !alive[l.Namespace+"/"+l.Pod] {
			l.Gone = true
		}
	}
}

func (t *k8sTarget) fetch(ctx context.Context, c *k8sCollector, g podGroup, pod, container string, started time.Time, previous bool) {
	raw, err := t.p.kube.PodLogsSince(ctx, g.ns, pod, container, started, LogTailLines, previous)
	if err != nil {
		return // ex.: o container ainda está subindo; a próxima coleta pega
	}
	lines := parseLogLines(raw, started)
	key := g.ns + "/" + pod + "/" + container
	if previous {
		key += "/previous"
	}
	c.logs[key] = &store.PodLog{Namespace: g.ns, Pod: pod, Container: container, Role: g.role,
		Previous: previous, Truncated: len(lines) >= LogTailLines, Lines: lines}
}

func (t *k8sTarget) Diagnostics(ctx context.Context, started time.Time) store.Diagnostics {
	t.Snapshot(ctx, started)
	c := &t.diag
	c.mu.Lock()
	defer c.mu.Unlock()
	d := store.Diagnostics{Logs: []store.PodLog{}, Events: []store.KubeEvent{}, Notes: append([]string(nil), c.notes...)}
	for _, l := range c.logs {
		d.Logs = append(d.Logs, *l)
	}
	sort.Slice(d.Logs, func(i, j int) bool {
		a, b := d.Logs[i], d.Logs[j]
		if (a.Role == "alvo") != (b.Role == "alvo") {
			return a.Role == "alvo"
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		if a.Pod != b.Pod {
			return a.Pod < b.Pod
		}
		if a.Container != b.Container {
			return a.Container < b.Container
		}
		return a.Previous && !b.Previous
	})
	nss := map[string]bool{}
	for _, g := range c.groups {
		if nss[g.ns] {
			continue
		}
		nss[g.ns] = true
		evs, err := t.p.kube.ListEvents(ctx, g.ns)
		if err != nil {
			d.Notes = append(d.Notes, fmt.Sprintf("eventos de %s: %v", g.ns, err))
			continue
		}
		for _, e := range evs {
			at := e.When()
			if at.Before(started.Add(-30 * time.Second)) {
				continue
			}
			count := e.Count
			if e.Series != nil && e.Series.Count > count {
				count = e.Series.Count
			}
			d.Events = append(d.Events, store.KubeEvent{
				T: math.Round(at.Sub(started).Seconds()*10) / 10, Type: e.Type, Reason: e.Reason, Namespace: g.ns,
				Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name, Message: strings.TrimSpace(e.Message), Count: count,
			})
		}
	}
	d.Events = capEvents(d.Events, MaxEvents)
	return d
}

// capEvents ordena por instante e, acima de max, fica com os Warning e os mais recentes.
func capEvents(evs []store.KubeEvent, max int) []store.KubeEvent {
	if len(evs) > max {
		sort.SliceStable(evs, func(i, j int) bool {
			if (evs[i].Type == "Warning") != (evs[j].Type == "Warning") {
				return evs[i].Type == "Warning"
			}
			return evs[i].T > evs[j].T
		})
		evs = evs[:max]
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].T < evs[j].T })
	return evs
}

// deps lista as dependências usadas pelas falhas do cenário.
func deps(sc scenario.Scenario) []scenario.Dependency {
	var out []scenario.Dependency
	for _, f := range sc.Faults {
		if f.Dependency != nil {
			out = append(out, *f.Dependency)
		}
	}
	return out
}

// --- Docker ---

func (t *dockerTarget) Snapshot(ctx context.Context, started time.Time) {} // stop/start mantém o log

func (t *dockerTarget) Diagnostics(ctx context.Context, started time.Time) store.Diagnostics {
	d := store.Diagnostics{Logs: []store.PodLog{}, Events: []store.KubeEvent{}}
	raw, err := t.p.docker.LogsSince(ctx, t.id, started, LogTailLines)
	if err != nil {
		d.Notes = append(d.Notes, "logs do container: "+err.Error())
		return d
	}
	lines := parseLogLines(raw, started)
	d.Logs = append(d.Logs, store.PodLog{Pod: t.name, Role: "alvo", Truncated: len(lines) >= LogTailLines, Lines: lines})
	return d
}
