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
	// Snapshot acompanha os pods novos (no Kubernetes, abre o stream do log de cada um). Roda
	// antes de cada falha e de tempos em tempos.
	Snapshot(ctx context.Context, started time.Time)
	// Diagnostics faz a última coleta e devolve tudo, no fim da execução.
	Diagnostics(ctx context.Context, started time.Time) store.Diagnostics
}

const (
	// LogTailLines limita as linhas guardadas por container (as mais recentes).
	LogTailLines = 3000
	// MaxEvents limita os eventos guardados (os Warning têm prioridade).
	MaxEvents = 300
	// SnapshotEvery é o intervalo em que o app procura pods novos (para abrir o stream do log).
	SnapshotEvery = 5 * time.Second
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
	// Um stream de log (follow) por instância de container: ns/pod/container#restarts.
	streams map[string]bool
	stop    context.CancelFunc
	sctx    context.Context
	wg      sync.WaitGroup
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

// Snapshot acha os pods do alvo e das dependências e abre um stream de log (follow) para cada
// container novo. O stream fica aberto até o container terminar, então pega até as últimas
// linhas de um pod apagado pelo kill.
func (t *k8sTarget) Snapshot(ctx context.Context, started time.Time) {
	c := &t.diag
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logs == nil {
		c.logs = map[string]*store.PodLog{}
		c.streams = map[string]bool{}
		c.sctx, c.stop = context.WithTimeout(context.Background(), 6*time.Hour)
	}
	alive := map[string]bool{}
	listed := map[string]bool{} // grupos (role) cuja lista de pods veio nesta coleta
	for _, g := range t.groupsOf(ctx) {
		pods, err := t.p.kube.ListPods(ctx, g.ns, g.selector)
		if err != nil {
			continue // tenta de novo na próxima coleta
		}
		listed[g.role] = true
		for _, p := range pods {
			alive[g.ns+"/"+p.Metadata.Name] = true
			for _, cs := range p.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.RestartCount == 0 {
					continue // ainda não subiu: a próxima coleta pega
				}
				key := fmt.Sprintf("%s/%s/%s#%d", g.ns, p.Metadata.Name, cs.Name, cs.RestartCount)
				if c.streams[key] {
					continue
				}
				first := !c.streams[fmt.Sprintf("%s/%s/%s#%d", g.ns, p.Metadata.Name, cs.Name, cs.RestartCount-1)]
				c.streams[key] = true
				if cs.RestartCount > 0 && first {
					// Reiniciou antes de ser visto: guarda o log da instância anterior.
					t.fetchPrevious(ctx, c, g, p.Metadata.Name, cs.Name, started)
				}
				t.follow(c, g, p.Metadata.Name, cs.Name, started)
			}
		}
	}
	// Só conclui que um pod sumiu quando a lista do grupo dele veio (uma coleta cancelada no
	// fim da execução não pode marcar todos como apagados).
	for _, l := range c.logs {
		if listed[l.Role] {
			l.Gone = !alive[l.Namespace+"/"+l.Pod]
		}
	}
}

func (t *k8sTarget) entry(c *k8sCollector, g podGroup, pod, container string, previous bool) *store.PodLog {
	key := g.ns + "/" + pod + "/" + container
	if previous {
		key += "/previous"
	}
	l := c.logs[key]
	if l == nil {
		l = &store.PodLog{Namespace: g.ns, Pod: pod, Container: container, Role: g.role, Previous: previous, Lines: []store.LogLine{}}
		c.logs[key] = l
	}
	return l
}

// follow abre o stream de um container; as linhas vão para o mesmo registro do pod (depois de
// um reinício, a instância nova continua no mesmo log).
func (t *k8sTarget) follow(c *k8sCollector, g podGroup, pod, container string, started time.Time) {
	t.entry(c, g, pod, container, false)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		_ = t.p.kube.FollowPodLogs(c.sctx, g.ns, pod, container, started, func(line string) {
			ls := parseLogLines(line, started)
			c.mu.Lock()
			l := t.entry(c, g, pod, container, false)
			l.Lines = append(l.Lines, ls...)
			if n := len(l.Lines); n > LogTailLines {
				l.Lines = append(l.Lines[:0:0], l.Lines[n-LogTailLines:]...)
				l.Truncated = true
			}
			c.mu.Unlock()
		})
	}()
}

func (t *k8sTarget) fetchPrevious(ctx context.Context, c *k8sCollector, g podGroup, pod, container string, started time.Time) {
	raw, err := t.p.kube.PodLogsSince(ctx, g.ns, pod, container, started, LogTailLines, true)
	if err != nil {
		return
	}
	l := t.entry(c, g, pod, container, true)
	l.Lines = parseLogLines(raw, started)
	l.Truncated = len(l.Lines) >= LogTailLines
}

func (t *k8sTarget) Diagnostics(ctx context.Context, started time.Time) store.Diagnostics {
	t.Snapshot(ctx, started)
	c := &t.diag
	// Dá um instante para as últimas linhas chegarem e fecha os streams.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	if c.stop != nil {
		c.stop()
	}
	c.wg.Wait()
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
			if at.Before(started.Add(-30*time.Second)) || chaosNoise(e) {
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

// chaosNoise diz se o evento é só a contabilidade interna do Chaos Mesh (Updated,
// FinalizerInited...). Dos recursos do Chaos Mesh ficam só aplicar, recuperar e os erros.
func chaosNoise(e kube.Event) bool {
	if !strings.HasSuffix(e.InvolvedObject.Kind, "Chaos") {
		return false
	}
	switch e.Reason {
	case "Applied", "Recovered", "Failed":
		return false
	}
	return e.Type != "Warning"
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
