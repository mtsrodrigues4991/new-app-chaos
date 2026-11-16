// Package fix leva uma correção sugerida pela IA até um PR em rascunho: uma pessoa aprova a
// tentativa, o sandbox aplica o patch (com o agente, quando é preciso), roda o build e os testes
// do repositório, e só então um passo sem modelo publica o branch e abre o PR.
//
// O que este pacote garante, por código e não por prompt:
//   - nada roda sem aprovação;
//   - todo diff (o proposto e o que sai de cada rodada) passa pelos limites de escopo;
//   - nenhum PR é aberto sem o build e os testes passarem;
//   - o PR é sempre rascunho, num branch nac/, e o merge é sempre de uma pessoa;
//   - cada tentativa guarda quem pediu, quem aprovou, os diffs, os logs e o custo.
package fix

import (
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ghpr"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/patch"
)

// Status é o estado de uma tentativa.
type Status string

const (
	AwaitingApproval Status = "awaiting_approval" // criada; ninguém aprovou ainda
	Queued           Status = "queued"            // aprovada; esperando a vez
	Running          Status = "running"           // rodando no sandbox
	PROpen           Status = "pr_open"           // build e testes passaram; PR em rascunho aberto
	Rejected         Status = "rejected"          // uma pessoa recusou
	Failed           Status = "failed"            // não chegou a um PR (o motivo fica em Error)
)

// Done diz se a tentativa terminou.
func (s Status) Done() bool { return s == PROpen || s == Rejected || s == Failed }

// Round é uma rodada no sandbox.
type Round struct {
	N         int       `json:"n"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	// Applied: o patch de entrada aplicou como veio. AgentRan: o agente precisou editar.
	Applied     bool    `json:"applied"`
	AgentRan    bool    `json:"agentRan"`
	AgentOutput string  `json:"agentOutput,omitempty"`
	AgentError  string  `json:"agentError,omitempty"`
	CostUSD     float64 `json:"costUsd,omitempty"`
	Turns       int     `json:"turns,omitempty"`
	// Task são as instruções dadas ao agente nesta rodada (auditoria).
	Task string `json:"task,omitempty"`
	// Diff é o que a rodada produziu; Review, o parecer de escopo sobre ele.
	Diff     string        `json:"diff"`
	Review   *patch.Review `json:"review,omitempty"`
	TestExit int           `json:"testExit"`
	TestLog  string        `json:"testLog,omitempty"`
	Error    string        `json:"error,omitempty"`
}

// Attempt é uma tentativa de transformar uma correção sugerida num PR.
type Attempt struct {
	ID         string `json:"id"`
	RunID      string `json:"runId"`
	ScenarioID string `json:"scenarioId"`
	// A correção, como a IA sugeriu no relatório da execução.
	FixIndex int    `json:"fixIndex"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Metric   string `json:"metric,omitempty"`   // o que deve melhorar
	Expected string `json:"expected,omitempty"` // quanto
	// De onde vem o código e para onde vai o PR.
	Repo       string `json:"repo"`
	BaseBranch string `json:"baseBranch"`
	Commit     string `json:"commit"`         // commit que rodou no teste: a base do patch
	Path       string `json:"path,omitempty"` // subpasta da aplicação
	// Como o repositório é construído e testado.
	TestImage   string `json:"testImage"`
	TestCommand string `json:"testCommand"`
	Model       string `json:"model"`
	// Proposed é o diff sugerido (relativo a Path); Review, o parecer de escopo sobre ele.
	Proposed string       `json:"proposed"`
	Review   patch.Review `json:"review"`

	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
	// Quem pediu e quem aprovou ("" com o login desligado).
	CreatedBy string     `json:"createdBy,omitempty"`
	DecidedBy string     `json:"decidedBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Rounds    []Round    `json:"rounds"`
	FinalDiff string     `json:"finalDiff,omitempty"` // o que foi para o PR, relativo à raiz do repositório
	CostUSD   float64    `json:"costUsd"`
	PR        *ghpr.PR   `json:"pr,omitempty"`
	// Cycle é o que aconteceu depois do PR: build das versões, experimento A/B e comentário (nil = não começou).
	Cycle      *Cycle   `json:"cycle,omitempty"`
	Highlights []string `json:"highlights,omitempty"`
}

// Limites de uma tentativa.
const (
	// MaxRounds é quantas vezes o agente pode tentar fazer o build passar.
	MaxRounds = 3
	// MaxCostUSD é o teto de gasto com o agente por tentativa.
	MaxCostUSD = 3.0
	// RoundTimeout é o prazo de cada rodada (download, agente, build e testes).
	RoundTimeout = 30 * time.Minute
)
