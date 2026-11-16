package fix

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ephemeral"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/experiment"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ghpr"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/stats"
)

// Stage é a etapa do ciclo depois do PR.
type Stage string

const (
	StageBuilding  Stage = "building"  // o CI do repositório constrói as imagens de A e de B
	StageDeploying Stage = "deploying" // as duas cópias sobem no ambiente efêmero
	StageTesting   Stage = "testing"   // o experimento A/B roda
	StageDone      Stage = "done"      // resultado comentado no PR
	StageFailed    Stage = "failed"    // o ciclo não chegou a um resultado (o PR continua aberto)
	StageSkipped   Stage = "skipped"   // o ciclo não se aplica a esta tentativa (Note diz por quê)
)

// Cycle é o que acontece depois que a correção virou PR: provar (ou não) o ganho.
type Cycle struct {
	Stage Stage  `json:"stage"`
	Note  string `json:"note,omitempty"`
	// Imagens construídas pelo CI do repositório: A é o commit testado, B é o branch da correção.
	ImageA string         `json:"imageA,omitempty"`
	ImageB string         `json:"imageB,omitempty"`
	BuildA *ghpr.BuildRun `json:"buildA,omitempty"`
	BuildB *ghpr.BuildRun `json:"buildB,omitempty"`
	// O experimento e a decisão dele.
	ExperimentID string        `json:"experimentId,omitempty"`
	Outcome      stats.Outcome `json:"outcome,omitempty"`
	// Commented: o resultado foi escrito no PR. CleanedUp: as cópias foram apagadas do cluster.
	Commented bool `json:"commented"`
	CleanedUp bool `json:"cleanedUp"`
}

// Builder constrói imagens no CI do repositório alvo e comenta no PR (o ghpr.Client).
type Builder interface {
	BuildImage(ctx context.Context, b ghpr.Build, poll time.Duration) (ghpr.BuildRun, error)
	Comment(ctx context.Context, repo string, number int, body string) error
}

// Envs sobe e apaga as cópias A e B (o ephemeral.Kube).
type Envs interface {
	Ready() string
	Create(ctx context.Context, id string, src scenario.Target, imageA, imageB string) (ephemeral.Env, error)
	Delete(ctx context.Context, id string) error
}

// Experiments roda o experimento (o experiment.Manager).
type Experiments interface {
	Start(ctx context.Context, spec experiment.Spec, sc scenario.Scenario) (experiment.Experiment, error)
	Get(ctx context.Context, id string) (experiment.Experiment, error)
}

// Scenarios lê o cenário atual (o store.Store).
type Scenarios interface {
	GetScenario(ctx context.Context, id string) (scenario.Scenario, error)
}

// Prazos do ciclo.
const (
	BuildTimeout      = 25 * time.Minute
	ExperimentTimeout = 3 * time.Hour
)

// MetricFor traduz o texto "o que deve melhorar" da correção para a métrica do experimento.
// Na dúvida, p95: é a métrica dos SLOs.
func MetricFor(text string, sc scenario.Scenario) experiment.Metric {
	t := strings.ToLower(text)
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(t, p) {
				return true
			}
		}
		return false
	}
	if !sc.HasLoad() {
		return experiment.Metric{Name: experiment.MetricScore}
	}
	switch {
	case has("p99"):
		return experiment.Metric{Name: experiment.MetricP99}
	case has("p50", "mediana"):
		return experiment.Metric{Name: experiment.MetricP50}
	case has("error_rate", "taxa de erro", "erros", "5xx"):
		return experiment.Metric{Name: experiment.MetricErrorRate}
	case has("score", "nota") && sc.HasChaos():
		return experiment.Metric{Name: experiment.MetricScore}
	case has("avg", "média", "media"):
		return experiment.Metric{Name: experiment.MetricAvg}
	}
	return experiment.Metric{Name: experiment.MetricP95}
}

func (m *Manager) pollEvery() time.Duration {
	if m.CyclePoll > 0 {
		return m.CyclePoll
	}
	return 5 * time.Second
}

// cycle roda depois do PR aberto. Nada aqui muda o estado da tentativa (o PR existe e continua
// existindo): o que acontece fica em a.Cycle. As cópias são apagadas em qualquer desfecho.
func (m *Manager) cycle(ctx context.Context, a *Attempt) {
	skip := func(why string) {
		a.Cycle = &Cycle{Stage: StageSkipped, Note: why, CleanedUp: true}
		m.save(a)
	}
	if m.Experiments == nil || m.Builder == nil || m.Envs == nil || m.Scenarios == nil {
		skip("o ciclo de re-teste não está configurado neste ambiente")
		return
	}
	sc, err := m.Scenarios.GetScenario(ctx, a.ScenarioID)
	if err != nil {
		skip("o cenário da execução não existe mais")
		return
	}
	if sc.Code == nil || sc.Code.BuildWorkflow == "" || sc.Code.Image == "" {
		skip("o cenário não diz como construir a imagem (workflow de build e imagem, em \"Código da aplicação\"): o ciclo para no PR")
		return
	}
	if sc.Target.EffectivePlatform() != scenario.PlatformKubernetes {
		skip("o re-teste automático só existe para alvos no Kubernetes")
		return
	}
	if why := m.Envs.Ready(); why != "" {
		skip(why)
		return
	}
	spec := experiment.Spec{ScenarioID: sc.ID, Name: "Correção: " + a.Title, Metric: MetricFor(a.Metric, sc)}
	spec.Normalize()
	if err := spec.Validate(sc); err != nil {
		skip("o cenário não serve para um experimento A/B: " + err.Error())
		return
	}

	c := &Cycle{Stage: StageBuilding, CleanedUp: true} // nada no cluster ainda
	a.Cycle = c
	m.save(a)
	fail := func(what string, err error) {
		c.Stage, c.Note = StageFailed, what+": "+err.Error()
		m.save(a)
		// O PR fica sabendo que o re-teste não aconteceu: silêncio pareceria "sem diferença".
		body := fmt.Sprintf("**new-app-chaos:** o re-teste automático desta correção não chegou a um resultado.\n\n%s: %s\n\nO ganho desta mudança **não foi medido**.", what, err.Error())
		if m.Builder.Comment(ctx, a.Repo, a.PR.Number, body) == nil {
			c.Commented = true
			m.save(a)
		}
	}

	// As duas versões saem do mesmo pipeline: comparar a imagem que já estava implantada com uma
	// recém-construída mediria também a diferença de build.
	c.ImageA = fmt.Sprintf("%s:nac-base-%s", sc.Code.Image, a.Commit[:12])
	c.ImageB = fmt.Sprintf("%s:nac-fix-%s", sc.Code.Image, a.ID)
	for _, b := range []struct {
		ref, image string
		run        **ghpr.BuildRun
	}{{a.Commit, c.ImageA, &c.BuildA}, {a.PR.Branch, c.ImageB, &c.BuildB}} {
		bctx, cancel := context.WithTimeout(ctx, BuildTimeout)
		run, err := m.Builder.BuildImage(bctx, ghpr.Build{Repo: a.Repo, Workflow: sc.Code.BuildWorkflow, DispatchRef: a.BaseBranch, Ref: b.ref, Tag: b.image[strings.LastIndex(b.image, ":")+1:]}, m.pollEvery())
		cancel()
		if run.ID != 0 {
			*b.run = &run
		}
		if err != nil {
			fail("O build da imagem não terminou", err)
			return
		}
		m.save(a)
	}

	c.Stage, c.CleanedUp = StageDeploying, false
	m.save(a)
	// Daqui em diante há objetos no cluster: apagar é a última coisa que acontece, sempre.
	defer func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := m.Envs.Delete(bg, a.ID); err != nil {
			c.Note = strings.TrimSpace(c.Note + " As cópias A e B não foram apagadas do cluster: " + err.Error())
		} else {
			c.CleanedUp = true
		}
		m.save(a)
	}()
	env, err := m.Envs.Create(ctx, a.ID, sc.Target, c.ImageA, c.ImageB)
	if err != nil {
		fail("As versões A e B não subiram", err)
		return
	}

	c.Stage = StageTesting
	m.save(a)
	scA := sc
	scA.Target = env.A
	spec.B = &env.B
	ectx, cancel := context.WithTimeout(ctx, ExperimentTimeout)
	defer cancel()
	var e experiment.Experiment
	for { // outro experimento em andamento: espera a vez
		e, err = m.Experiments.Start(ectx, spec, scA)
		if !errors.Is(err, experiment.ErrBusy) {
			break
		}
		select {
		case <-ectx.Done():
			err = ectx.Err()
		case <-time.After(m.pollEvery()):
			continue
		}
		break
	}
	if err != nil {
		fail("O experimento não começou", err)
		return
	}
	c.ExperimentID = e.ID
	m.save(a)
	for !e.Status.Done() {
		select {
		case <-ectx.Done():
			fail("O experimento não terminou a tempo", ectx.Err())
			return
		case <-time.After(m.pollEvery()):
		}
		if got, err := m.Experiments.Get(ectx, e.ID); err == nil {
			e = got
		}
	}
	if e.Status != experiment.Completed || e.Result == nil {
		fail("O experimento não chegou a uma decisão", fmt.Errorf("%s %s", e.Status, e.Error))
		return
	}
	c.Outcome = e.Result.Outcome
	if err := m.Builder.Comment(ctx, a.Repo, a.PR.Number, resultComment(*a, e, m.AppURL)); err != nil {
		c.Stage, c.Note = StageFailed, "o experimento decidiu, mas o comentário no PR falhou: "+err.Error()
		m.save(a)
		return
	}
	c.Stage, c.Commented = StageDone, true
	m.save(a)
}

var outcomeText = map[stats.Outcome]string{
	stats.Improved:     "✅ **Melhorou**",
	stats.Worsened:     "❌ **Piorou**",
	stats.NoDifference: "➖ **Sem diferença**",
	stats.Inconclusive: "❔ **Inconclusivo**",
}

var metricText = map[string]string{
	experiment.MetricP50: "p50 (ms)", experiment.MetricP95: "p95 (ms)", experiment.MetricP99: "p99 (ms)", experiment.MetricAvg: "latência média (ms)",
	experiment.MetricErrorRate: "taxa de erro (%)", experiment.MetricScore: "nota de resiliência", experiment.MetricCPUPerRequest: "CPU por requisição",
}

// resultComment é o comentário do resultado no PR: a decisão, os números e o que a limita.
func resultComment(a Attempt, e experiment.Experiment, appURL string) string {
	r := e.Result
	var b strings.Builder
	fmt.Fprintf(&b, "## Resultado do experimento A/B: %s\n\n", outcomeText[r.Outcome])
	b.WriteString("O new-app-chaos rodou o mesmo teste contra a versão atual (A) e contra este PR (B), em execuções repetidas e intercaladas. A decisão é uma regra estatística, **sem IA**.\n\n")
	b.WriteString("| Métrica | A (mediana) | B (mediana) | Diferença | Intervalo de 95% | Decisão |\n|---|---|---|---|---|---|\n")
	row := func(name string, c stats.Comparison) {
		fmt.Fprintf(&b, "| %s | %.2f | %.2f | %+.2f (%+.1f%%) | %+.2f a %+.2f | %s |\n", name, c.MedianA, c.MedianB, c.Diff, c.DiffPct, c.Lo, c.Hi, outcomeText[c.Outcome])
	}
	row("**"+metricText[e.Metric.Name]+"** (principal)", r.Primary)
	for _, g := range r.Guards {
		row("guarda: "+metricText[g.Metric], g.Comparison)
	}
	fmt.Fprintf(&b, "\n- Execuções válidas: %d de A e %d de B (%d no total).\n", r.Primary.NA, r.Primary.NB, len(e.Runs))
	if a.Expected != "" {
		fmt.Fprintf(&b, "- Hipótese da correção: %s.\n", strings.TrimRight(a.Expected, "."))
	}
	if r.NoisePct != nil {
		fmt.Fprintf(&b, "- Piso de ruído do cenário (último teste A/A): %.1f%%.\n", *r.NoisePct)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	if appURL != "" {
		fmt.Fprintf(&b, "\nDetalhes: %s/#/experimentos/%s\n", strings.TrimRight(appURL, "/"), e.ID)
	}
	b.WriteString("\nAs cópias usadas no teste foram apagadas do cluster. O merge continua sendo uma decisão sua.\n")
	return b.String()
}
