package ai

import (
	"context"
	"errors"
)

// Claude implementa LLMProvider usando a API da Anthropic.
// A chave vem da variável de ambiente ANTHROPIC_API_KEY e fica só no backend.
type Claude struct {
	APIKey string
	Model  string
}

// ErrNotImplemented marca o que ainda será feito no marco 4 (IA).
var ErrNotImplemented = errors.New("ainda não implementado")

func (c *Claude) Analyze(ctx context.Context, ev Evidence) (Report, error) {
	return Report{}, ErrNotImplemented
}

func (c *Claude) Chat(ctx context.Context, ev Evidence, history []Message) (Message, error) {
	return Message{}, ErrNotImplemented
}
