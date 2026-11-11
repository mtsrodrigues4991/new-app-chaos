// Package ai define a camada de IA trocável que analisa os resultados.
package ai

import "context"

// Evidence é o pacote enviado ao modelo: só agregados, nunca métricas brutas.
type Evidence struct {
	ScenarioJSON string `json:"scenario"` // cenário em JSON
	WindowsJSON  string `json:"windows"`  // métricas agregadas por janela
	FaultsJSON   string `json:"faults"`   // falhas com início/fim reais
	PreviousJSON string `json:"previous"` // resumos de execuções anteriores
	Verdict      string `json:"verdict"`  // calculado pelo backend a partir dos SLOs
	LogsExcerpt  string `json:"logs"`     // trecho de logs do alvo
}

// Report é a saída estruturada esperada do modelo (validada por schema).
type Report struct {
	Summary       string   `json:"summary"`
	BreakingPoint string   `json:"breakingPoint"`
	Regressions   []string `json:"regressions"`
	Causes        []string `json:"causes"`
	Fixes         []string `json:"fixes"`
	Score         int      `json:"score"` // 0 a 100
}

// Message é uma mensagem do chat sobre o resultado.
type Message struct {
	Role    string `json:"role"` // "user" ou "assistant"
	Content string `json:"content"`
}

// LLMProvider é implementado por cada provedor (Claude no MVP; OpenAI, local depois).
type LLMProvider interface {
	Analyze(ctx context.Context, ev Evidence) (Report, error)
	Chat(ctx context.Context, ev Evidence, history []Message) (Message, error)
}
