// Package ai é a camada de provedor de IA, trocável. Ela só sabe mandar instruções, um bloco
// de contexto e mensagens a um modelo e devolver o texto. O que mandar (evidências da execução,
// formato do relatório) fica em internal/analysis, igual para qualquer provedor.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Message é uma mensagem da conversa.
type Message struct {
	Role    string `json:"role"` // "user" ou "assistant"
	Content string `json:"content"`
}

// Request é um pedido ao modelo.
type Request struct {
	Model    string
	System   string // instruções fixas
	Context  string // bloco grande e estável (evidências); o provedor pode guardá-lo em cache
	Messages []Message
	// Schema, quando presente, obriga a resposta a ser um JSON nesse JSON Schema.
	Schema    map[string]any
	MaxTokens int
	// Tools são ferramentas que o modelo pode chamar antes de responder (ex.: ler o código).
	Tools []Tool
	// MaxTurns limita as idas e voltas com ferramentas (padrão 40).
	MaxTurns int
}

// Tool é uma ferramenta executada pelo app quando o modelo pede.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any // propriedades do JSON Schema da entrada
	Required    []string
	// Run devolve o texto do resultado; um erro vira resultado de erro para o modelo (não aborta).
	Run func(ctx context.Context, input json.RawMessage) (string, error)
}

// Response é a resposta do modelo.
type Response struct {
	Text            string `json:"-"`
	Model           string `json:"model"` // modelo que de fato respondeu (pode ser o substituto)
	InputTokens     int64  `json:"inputTokens"`
	OutputTokens    int64  `json:"outputTokens"`
	CacheReadTokens int64  `json:"cacheReadTokens,omitempty"`
	ToolCalls       int    `json:"toolCalls,omitempty"`
}

// Provider é implementado por cada provedor (Anthropic no MVP).
type Provider interface {
	Info() ProviderInfo
	Generate(ctx context.Context, req Request) (Response, error)
}

// ProviderInfo descreve um provedor para a tela de Configurações.
type ProviderInfo struct {
	ID           string      `json:"id"`
	Label        string      `json:"label"`
	KeyEnv       string      `json:"keyEnv"`     // variável de ambiente da chave
	Configured   bool        `json:"configured"` // a chave está definida
	DefaultModel string      `json:"defaultModel"`
	Models       []ModelInfo `json:"models"`
}

// ModelInfo é um modelo oferecido na tela de Configurações.
type ModelInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

var (
	// ErrNotConfigured: falta a chave do provedor no .env.
	ErrNotConfigured = errors.New("provedor de IA sem chave configurada")
	// ErrUnknownProvider: o provedor salvo nas Configurações não existe.
	ErrUnknownProvider = errors.New("provedor de IA desconhecido")
)

// RefusalError: o modelo recusou responder (filtro de segurança do provedor).
type RefusalError struct{ Reason string }

func (e *RefusalError) Error() string {
	if e.Reason == "" {
		return "o modelo recusou responder"
	}
	return "o modelo recusou responder: " + e.Reason
}

// TruncatedError: a resposta passou do limite de tokens.
type TruncatedError struct{ MaxTokens int }

func (e *TruncatedError) Error() string {
	return fmt.Sprintf("a resposta passou do limite de %d tokens", e.MaxTokens)
}

// Registry guarda os provedores disponíveis.
type Registry struct{ providers []Provider }

func NewRegistry(ps ...Provider) *Registry { return &Registry{providers: ps} }

// List descreve todos os provedores.
func (r *Registry) List() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Info())
	}
	return out
}

// Get devolve o provedor pelo id.
func (r *Registry) Get(id string) (Provider, error) {
	for _, p := range r.providers {
		if p.Info().ID == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, id)
}

// Default é o primeiro provedor registrado.
func (r *Registry) Default() ProviderInfo { return r.providers[0].Info() }
