// Package experiment roda um experimento A/B: o mesmo cenário contra duas versões (A e B),
// em execuções repetidas e intercaladas, e decide com uma regra estatística se B melhorou,
// piorou ou não difere de A. A decisão é do pacote stats, sem IA; a IA só explica.
//
// Quem implanta a versão B é o usuário: A é o alvo do cenário e B é outro alvo (outro container
// ou Deployment) com a versão candidata. Sem B, é um teste A/A: a mesma versão dos dois lados,
// que mede o piso de ruído do cenário naquele ambiente.
package experiment

import (
	"fmt"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/stats"
)

// Status é o estado de um experimento.
type Status string

const (
	Pending   Status = "pending"
	Running   Status = "running"
	Completed Status = "completed"
	Aborted   Status = "aborted"
	Failed    Status = "failed"
)

// Done diz se o experimento terminou.
func (s Status) Done() bool { return s != Pending && s != Running }

// Métricas que um experimento pode comparar. A principal é declarada antes de rodar: olhar
// várias depois e escolher a que melhorou é erro conhecido.
const (
	MetricP50       = "p50"        // ms, janela estável
	MetricP95       = "p95"        // ms, janela estável
	MetricP99       = "p99"        // ms, janela estável
	MetricAvg       = "avg"        // ms, janela estável
	MetricErrorRate = "error_rate" // %, janela estável
	MetricScore     = "score"      // nota de resiliência (0 a 100)
	// Só como guarda.
	MetricCPUPerRequest = "cpu_per_request" // % de CPU do alvo por requisição por segundo
)

// Metric é a métrica principal do experimento.
type Metric struct {
	Name string `json:"name"`
	// Endpoint restringe a métrica de latência ou erro a um endpoint (índice na lista do
	// cenário); nil = todos os endpoints juntos.
	Endpoint *int `json:"endpoint,omitempty"`
}

func latencyMetric(name string) bool {
	return name == MetricP50 || name == MetricP95 || name == MetricP99 || name == MetricAvg
}

// Direction diz para que lado a métrica melhora.
func Direction(name string) stats.Direction {
	if name == MetricScore {
		return stats.HigherIsBetter
	}
	return stats.LowerIsBetter
}

// MarginFor é o limite prático de cada métrica. pct é o limite do experimento (sugestão: 5%).
// O piso absoluto cobre medianas perto de zero (0,1 ponto percentual de erro; 1 ponto de nota;
// 0,5 ms de latência, abaixo do que a medição distingue).
func MarginFor(name string, pct float64) stats.Margin {
	switch name {
	case MetricErrorRate:
		return stats.Margin{Pct: pct, Abs: 0.1}
	case MetricScore:
		return stats.Margin{Pct: pct, Abs: 1}
	case MetricCPUPerRequest:
		return stats.Margin{Pct: 2 * pct}
	}
	return stats.Margin{Pct: pct, Abs: 0.5}
}

// Spec é o que o usuário define ao criar o experimento.
type Spec struct {
	Name       string `json:"name"`
	ScenarioID string `json:"scenarioId"`
	// B é o alvo com a versão candidata. nil = teste A/A (o alvo do cenário dos dois lados).
	B      *scenario.Target `json:"b,omitempty"`
	Metric Metric           `json:"metric"`
	// MinRuns e MaxRuns são as execuções por versão: decide a partir de MinRuns válidas e para
	// em MaxRuns tentativas, com o resultado que houver.
	MinRuns int `json:"minRuns"`
	MaxRuns int `json:"maxRuns"`
	// MarginPct é o limite prático, em % da mediana de A.
	MarginPct float64 `json:"marginPct"`
	// CooldownSeconds é a pausa entre execuções, para o alvo esvaziar filas e conexões.
	CooldownSeconds int `json:"cooldownSeconds"`
}

// Limites e valores sugeridos.
const (
	DefaultMaxRuns   = 10
	DefaultMarginPct = 5
	DefaultCooldown  = 5
	LimitMaxRuns     = 30
)

// Normalize preenche os valores sugeridos.
func (s *Spec) Normalize() {
	if s.MinRuns == 0 {
		s.MinRuns = stats.MinRuns
	}
	if s.MaxRuns == 0 {
		s.MaxRuns = max(DefaultMaxRuns, s.MinRuns)
	}
	if s.MarginPct == 0 {
		s.MarginPct = DefaultMarginPct
	}
	if s.CooldownSeconds == 0 {
		s.CooldownSeconds = DefaultCooldown
	}
	if s.Metric.Name == "" {
		s.Metric.Name = MetricP95
	}
}

// Validate confere a especificação contra o cenário.
func (s Spec) Validate(sc scenario.Scenario) error {
	var errs []string
	if s.MinRuns < stats.MinRuns {
		errs = append(errs, fmt.Sprintf("são precisas pelo menos %d execuções por versão para decidir", stats.MinRuns))
	}
	if s.MaxRuns < s.MinRuns || s.MaxRuns > LimitMaxRuns {
		errs = append(errs, fmt.Sprintf("o teto de execuções por versão deve ficar entre o mínimo e %d", LimitMaxRuns))
	}
	if s.MarginPct <= 0 || s.MarginPct > 50 {
		errs = append(errs, "o limite prático deve ficar entre 0% e 50%")
	}
	if s.CooldownSeconds < 0 || s.CooldownSeconds > 600 {
		errs = append(errs, "a pausa entre execuções deve ficar entre 0 s e 600 s")
	}
	switch {
	case latencyMetric(s.Metric.Name) || s.Metric.Name == MetricErrorRate:
		if !sc.HasLoad() {
			errs = append(errs, "métricas de latência e erro pedem um cenário com carga")
		} else if !sc.Profile.OpenModel() {
			// No modelo fechado a versão mais rápida recebe mais carga: as latências não são comparáveis.
			errs = append(errs, "o cenário usa carga em VUs; para comparar duas versões, mude o perfil para taxa de chegada")
		}
		if e := s.Metric.Endpoint; e != nil && (*e < 0 || *e >= len(sc.Endpoints)) {
			errs = append(errs, "o endpoint escolhido não existe no cenário")
		}
	case s.Metric.Name == MetricScore:
		if !sc.HasChaos() {
			errs = append(errs, "a nota de resiliência pede um cenário com falhas")
		}
		if s.Metric.Endpoint != nil {
			errs = append(errs, "a nota de resiliência não é por endpoint")
		}
	default:
		errs = append(errs, fmt.Sprintf("métrica desconhecida: %q", s.Metric.Name))
	}
	if b := s.B; b != nil {
		if b.EffectivePlatform() != sc.Target.EffectivePlatform() {
			errs = append(errs, "A e B precisam estar na mesma plataforma (Docker ou Kubernetes)")
		}
		if b.Name() == sc.Target.Name() && b.BaseURL == sc.Target.BaseURL {
			errs = append(errs, "B é o mesmo alvo de A; para um teste A/A, deixe B vazio")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Arms dos experimentos.
const (
	ArmA = "A"
	ArmB = "B"
)

// ArmRun é uma execução de um dos lados e o que se mediu nela.
type ArmRun struct {
	Arm    string `json:"arm"`
	RunID  string `json:"runId,omitempty"`
	Status string `json:"status"`
	// Valid diz se a execução entra na comparação; Invalid, por que não.
	Valid   bool   `json:"valid"`
	Invalid string `json:"invalid,omitempty"`
	// Values são a métrica principal e as de guarda (ausente = não medida nesta execução).
	Values map[string]float64 `json:"values,omitempty"`
	// Version identifica o que rodou (imagens do alvo); Commit, quando a imagem o declara.
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// Guard é o resultado de uma métrica de guarda.
type Guard struct {
	Metric string `json:"metric"`
	stats.Comparison
}

// Result é a decisão do experimento.
type Result struct {
	Outcome stats.Outcome    `json:"outcome"`
	Primary stats.Comparison `json:"primary"`
	Guards  []Guard          `json:"guards"`
	// NoisePct é o piso de ruído da métrica principal: num teste A/A, o medido aqui; num A/B,
	// o do teste A/A mais recente do mesmo cenário (nil = nunca medido).
	NoisePct *float64 `json:"noisePct,omitempty"`
	// Notes explicam a decisão e o que a limita.
	Notes []string `json:"notes"`
}

// Experiment é um experimento e o seu andamento.
type Experiment struct {
	ID string `json:"id"`
	Spec
	// Scenario é a cópia do cenário como estava ao começar.
	Scenario  scenario.Scenario `json:"scenario"`
	Status    Status            `json:"status"`
	Error     string            `json:"error,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	EndedAt   *time.Time        `json:"endedAt,omitempty"`
	Runs      []ArmRun          `json:"runs"`
	Result    *Result           `json:"result,omitempty"`
}

// AA diz se é um teste A/A.
func (e Experiment) AA() bool { return e.B == nil }

// scenarioFor devolve o cenário de um dos lados: o de B troca só o alvo.
func (e Experiment) scenarioFor(arm string) scenario.Scenario {
	sc := e.Scenario
	if arm == ArmB && e.B != nil {
		sc.Target = *e.B
	}
	return sc
}
