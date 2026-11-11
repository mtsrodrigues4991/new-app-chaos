package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
)

// Anthropic implementa Provider com a API da Anthropic (modelos Claude).
// A chave vem de ANTHROPIC_API_KEY (.env) e nunca vai para o banco nem para o navegador.
type Anthropic struct {
	apiKey string
	client anthropic.Client
}

// NewAnthropic cria o provedor. Opções extras servem para testes (ex.: option.WithBaseURL).
func NewAnthropic(apiKey string, opts ...option.RequestOption) *Anthropic {
	opts = append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	return &Anthropic{apiKey: apiKey, client: anthropic.NewClient(opts...)}
}

// Modelos oferecidos na tela de Configurações. O padrão é o Opus mais recente.
var anthropicModels = []ModelInfo{
	{ID: "claude-opus-5-5", Label: "Claude Opus 5.5", Note: "padrão; análise mais completa"},
	{ID: "claude-sonnet-5-5", Label: "Claude Sonnet 5.5", Note: "mais barato, quase tão bom"},
	{ID: "claude-haiku-5-5", Label: "Claude Haiku 5.5", Note: "o mais rápido e barato"},
}

func (a *Anthropic) Info() ProviderInfo {
	return ProviderInfo{ID: "anthropic", Label: "Anthropic (Claude)", KeyEnv: "ANTHROPIC_API_KEY",
		Configured: a.apiKey != "", DefaultModel: anthropicModels[0].ID, Models: anthropicModels}
}

// withFallback: se o filtro de segurança recusar o pedido, a própria API refaz com o modelo
// substituto recomendado. O Haiku não tem substituto.
func withFallback(model string) bool { return !strings.Contains(model, "haiku") }

func (a *Anthropic) Generate(ctx context.Context, req Request) (Response, error) {
	if a.apiKey == "" {
		return Response{}, ErrNotConfigured
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	system := []anthropic.BetaTextBlockParam{{Text: req.System}}
	if req.Context != "" {
		// O contexto (evidências) é grande e se repete em cada pergunta do chat: fica em cache.
		system = append(system, anthropic.BetaTextBlockParam{Text: req.Context, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()})
	}
	msgs := make([]anthropic.BetaMessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			msgs = append(msgs, anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{
				Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(m.Content)}}})
		} else {
			msgs = append(msgs, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(m.Content)))
		}
	}
	params := anthropic.BetaMessageNewParams{
		Model:     req.Model,
		MaxTokens: int64(maxTokens),
		System:    system,
		Messages:  msgs,
		// Pensamento adaptativo com esforço médio: bom equilíbrio entre qualidade e custo.
		Thinking:     anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{}},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
	}
	if req.Schema != nil {
		params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: req.Schema}
	}
	if withFallback(req.Model) {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}

	// Streaming evita timeout em respostas longas; o texto final é montado no fim.
	stream := a.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return Response{}, err
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, friendlyError(err)
	}
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		return Response{}, &RefusalError{Reason: msg.StopDetails.Explanation}
	}
	if msg.StopReason == anthropic.BetaStopReasonMaxTokens {
		return Response{}, &TruncatedError{MaxTokens: maxTokens}
	}
	var b strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return Response{
		Text:            b.String(),
		Model:           string(msg.Model),
		InputTokens:     msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens,
		OutputTokens:    msg.Usage.OutputTokens,
		CacheReadTokens: msg.Usage.CacheReadInputTokens,
	}, nil
}

// friendlyError traduz os erros mais comuns da API.
func friendlyError(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.StatusCode {
	case 401:
		return fmt.Errorf("a chave ANTHROPIC_API_KEY foi recusada (401); confira o .env")
	case 403:
		return fmt.Errorf("a chave não tem permissão para este modelo (403): %s", apiErr.Message)
	case 404:
		return fmt.Errorf("modelo não encontrado (404); escolha outro em Configurações: %s", apiErr.Message)
	case 429:
		return fmt.Errorf("limite de uso da API atingido (429); tente de novo em instantes")
	case 529, 500, 502, 503:
		return fmt.Errorf("a API da Anthropic está instável (%d); tente de novo em instantes", apiErr.StatusCode)
	}
	return fmt.Errorf("API da Anthropic respondeu %d: %s", apiErr.StatusCode, apiErr.Message)
}
