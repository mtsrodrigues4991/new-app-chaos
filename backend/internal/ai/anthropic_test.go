package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// sse monta uma resposta em streaming mínima da API de mensagens.
func sse(text, stop string) string {
	ev := func(name string, v any) string {
		b, _ := json.Marshal(v)
		return fmt.Sprintf("event: %s\ndata: %s\n\n", name, b)
	}
	return ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{},
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 0, "cache_read_input_tokens": 40}}}) +
		ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}) +
		ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}}) +
		ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}) +
		ev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]any{"output_tokens": 7}}) +
		ev("message_stop", map[string]any{"type": "message_stop"})
}

func TestAnthropicRequestShape(t *testing.T) {
	var body map[string]any
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		beta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"ok":true}`, "end_turn"))
	}))
	defer srv.Close()

	p := NewAnthropic("chave-de-teste", option.WithBaseURL(srv.URL))
	resp, err := p.Generate(context.Background(), Request{
		Model: "claude-opus-5-5", System: "instruções", Context: "evidências",
		Messages: []Message{{Role: "user", Content: "oi"}, {Role: "assistant", Content: "olá"}, {Role: "user", Content: "e aí?"}},
		Schema:   map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"ok":true}` || resp.OutputTokens != 7 || resp.InputTokens != 140 || resp.Model != "claude-opus-5-5" {
		t.Fatalf("resposta inesperada: %+v", resp)
	}
	if !strings.Contains(beta, "server-side-fallback-2026-07-01") || body["fallbacks"] != "default" {
		t.Fatalf("esperava fallback padrão: beta=%q fallbacks=%v", beta, body["fallbacks"])
	}
	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "medium" || oc["format"] == nil {
		t.Fatalf("output_config inesperado: %v", oc)
	}
	if th, _ := body["thinking"].(map[string]any); th["type"] != "adaptive" {
		t.Fatalf("thinking inesperado: %v", body["thinking"])
	}
	sys, _ := body["system"].([]any)
	if len(sys) != 2 || sys[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("system deveria ter 2 blocos, o segundo em cache: %v", sys)
	}
	if msgs, _ := body["messages"].([]any); len(msgs) != 3 || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("mensagens inesperadas: %v", body["messages"])
	}
}

func TestAnthropicHaikuWithoutFallback(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse("ok", "end_turn"))
	}))
	defer srv.Close()
	p := NewAnthropic("k", option.WithBaseURL(srv.URL))
	if _, err := p.Generate(context.Background(), Request{Model: "claude-haiku-5-5", System: "s", Messages: []Message{{Role: "user", Content: "oi"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["fallbacks"]; ok {
		t.Fatalf("Haiku não tem substituto; fallbacks não deveria ir: %v", body["fallbacks"])
	}
}

func TestAnthropicErrors(t *testing.T) {
	if _, err := NewAnthropic("").Generate(context.Background(), Request{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("sem chave: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(fmt.Sprint(r.Header), "ruim") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse("", "refusal"))
	}))
	defer srv.Close()
	_, err := NewAnthropic("ruim", option.WithBaseURL(srv.URL), option.WithMaxRetries(0)).Generate(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("esperava erro 401 amigável: %v", err)
	}
	_, err = NewAnthropic("boa", option.WithBaseURL(srv.URL)).Generate(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}})
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("esperava recusa: %v", err)
	}
}
