package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ai"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/analysis"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/coderepo"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/verdict"
)

// Chaves das configurações de IA no banco. A chave da API nunca é salva: vem do .env.
const (
	settingProvider = "ai.provider"
	settingModel    = "ai.model"
)

// Uma análise ou resposta do chat pode levar alguns minutos (mais ainda lendo o código).
const aiTimeout = 10 * time.Minute

// busy evita duas chamadas de IA simultâneas para a mesma execução (clique duplo, duas abas).
var busy sync.Map

func lock(key string) (unlock func(), ok bool) {
	if _, loaded := busy.LoadOrStore(key, true); loaded {
		return nil, false
	}
	return func() { busy.Delete(key) }, true
}

func finished(r store.Run) bool { return r.Status != store.RunPending && r.Status != store.RunRunning }

func (s *Server) runData(ctx context.Context, id string) (analysis.RunData, error) {
	run, err := s.Store.GetRun(ctx, id)
	if err != nil {
		return analysis.RunData{}, err
	}
	ws, err := s.Store.Windows(ctx, id)
	if err != nil {
		return analysis.RunData{}, err
	}
	evs, err := s.Store.FaultEvents(ctx, id)
	if err != nil {
		return analysis.RunData{}, err
	}
	diag, err := s.Store.Diagnostics(ctx, id)
	if err != nil {
		return analysis.RunData{}, err
	}
	return analysis.RunData{Run: run, Windows: ws, Events: evs, Verdict: verdict.Evaluate(run.Scenario, ws, evs), Diagnostics: diag}, nil
}

// aiConfig devolve o provedor e o modelo escolhidos em Configurações.
func (s *Server) aiConfig(ctx context.Context) (ai.Provider, string, error) {
	def := s.AI.Default()
	pid, err := s.Store.Setting(ctx, settingProvider, def.ID)
	if err != nil {
		return nil, "", err
	}
	p, err := s.AI.Get(pid)
	if err != nil {
		return nil, "", err
	}
	model, err := s.Store.Setting(ctx, settingModel, p.Info().DefaultModel)
	if err != nil {
		return nil, "", err
	}
	if !p.Info().Configured {
		return nil, "", fmt.Errorf("%w: defina %s no arquivo .env e reinicie com docker compose up -d", ai.ErrNotConfigured, p.Info().KeyEnv)
	}
	return p, model, nil
}

// input monta as evidências: a execução, a referência do mesmo cenário e (opcional) o histórico.
func (s *Server) input(ctx context.Context, cur analysis.RunData, withHistory bool) (analysis.Input, error) {
	in := analysis.Input{Current: cur}
	refID, err := s.Store.Baseline(ctx, cur.Run.ScenarioID)
	if err != nil {
		return in, err
	}
	if refID != "" && refID != cur.Run.ID {
		ref, err := s.runData(ctx, refID)
		if err == nil && ref.Run.ScenarioID == cur.Run.ScenarioID {
			in.Reference = &ref
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return in, err
		}
	}
	if !withHistory {
		return in, nil
	}
	runs, err := s.Store.ListRuns(ctx, cur.Run.ScenarioID, 15)
	if err != nil {
		return in, err
	}
	for _, r := range runs {
		if !finished(r) {
			continue
		}
		d, err := s.runData(ctx, r.ID)
		if err != nil {
			return in, err
		}
		row := analysis.HistoryRowOf(d)
		row.Current, row.Reference = r.ID == cur.Run.ID, r.ID == refID
		in.History = append(in.History, row)
	}
	var rep analysis.Report
	if err := s.Store.Analysis(ctx, cur.Run.ID, &rep); err == nil {
		in.Report = &rep
	}
	return in, nil
}

// codeBrowser baixa (ou pega do cache) o código configurado no cenário. O cenário atual vale
// mais que a cópia da execução: o repositório pode ter sido configurado depois de rodar.
func (s *Server) codeBrowser(ctx context.Context, run store.Run) (*coderepo.Browser, error) {
	code := run.Scenario.Code
	if sc, err := s.Store.GetScenario(ctx, run.ScenarioID); err == nil && sc.Code != nil {
		code = sc.Code
	}
	if code == nil || code.Repo == "" || s.Code == nil {
		return nil, nil
	}
	snap, err := s.Code.Fetch(ctx, code.Repo, code.Branch, code.Path)
	if err != nil {
		return nil, fmt.Errorf("não consegui baixar o código de %s: %w", code.Repo, err)
	}
	return coderepo.NewBrowser(snap), nil
}

func aiStatus(err error) int {
	var refusal *ai.RefusalError
	switch {
	case errors.Is(err, ai.ErrNotConfigured), errors.Is(err, ai.ErrUnknownProvider):
		return http.StatusPreconditionFailed
	case errors.As(err, &refusal):
		return http.StatusUnprocessableEntity
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// POST /api/runs/{id}/analysis: gera (ou refaz) o relatório da IA.
func (s *Server) analyzeRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur, err := s.runData(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if !finished(cur.Run) {
		writeErr(w, http.StatusConflict, errors.New("espere a execução terminar para analisar"))
		return
	}
	if len(cur.Windows) == 0 {
		writeErr(w, http.StatusConflict, errors.New("a execução não tem métricas para analisar"))
		return
	}
	p, model, err := s.aiConfig(r.Context())
	if err != nil {
		writeErr(w, aiStatus(err), err)
		return
	}
	if !s.aiAllowed(w) {
		return
	}
	unlock, ok := lock("analysis:" + id)
	if !ok {
		writeErr(w, http.StatusConflict, errors.New("a análise desta execução já está em andamento"))
		return
	}
	defer unlock()
	in, err := s.input(r.Context(), cur, false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Segue mesmo se o navegador fechar: o relatório fica salvo para a próxima visita.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), aiTimeout)
	defer cancel()
	if in.Code, err = s.codeBrowser(ctx, cur.Run); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	rep, err := analysis.Analyze(ctx, p, model, in)
	if err != nil {
		writeErr(w, aiStatus(err), err)
		return
	}
	if err := s.Store.SaveAnalysis(ctx, id, rep); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// PUT /api/scenarios/{id}/reference {"runId": "..."}: marca ("" desmarca) a execução de referência.
func (s *Server) setReference(w http.ResponseWriter, r *http.Request) {
	scID := chi.URLParam(r, "id")
	var body struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("JSON inválido: %w", err))
		return
	}
	if body.RunID != "" {
		run, err := s.Store.GetRun(r.Context(), body.RunID)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		if run.ScenarioID != scID {
			writeErr(w, http.StatusBadRequest, errors.New("a referência precisa ser uma execução deste mesmo cenário"))
			return
		}
		if !finished(run) {
			writeErr(w, http.StatusConflict, errors.New("espere a execução terminar para marcá-la como referência"))
			return
		}
	}
	if err := s.Store.SetBaseline(r.Context(), scID, body.RunID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"referenceRunId": body.RunID})
}

// GET /api/runs/{id}/chat
func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.Store.ChatMessages(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// DELETE /api/runs/{id}/chat
func (s *Server) clearChat(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.ClearChat(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/runs/{id}/chat {"message": "..."}: salva a pergunta, pergunta à IA e salva a resposta.
func (s *Server) postChat(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("JSON inválido: %w", err))
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeErr(w, http.StatusBadRequest, errors.New("escreva uma pergunta"))
		return
	}
	cur, err := s.runData(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if !finished(cur.Run) {
		writeErr(w, http.StatusConflict, errors.New("espere a execução terminar para conversar sobre ela"))
		return
	}
	if !s.aiAllowed(w) {
		return
	}
	p, model, err := s.aiConfig(r.Context())
	if err != nil {
		writeErr(w, aiStatus(err), err)
		return
	}
	unlock, ok := lock("chat:" + id)
	if !ok {
		writeErr(w, http.StatusConflict, errors.New("espere a resposta anterior"))
		return
	}
	defer unlock()
	in, err := s.input(r.Context(), cur, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), aiTimeout)
	defer cancel()
	if in.Code, err = s.codeBrowser(ctx, cur.Run); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	q, err := s.Store.AddChatMessage(ctx, id, "user", body.Message)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	saved, err := s.Store.ChatMessages(ctx, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	history := make([]ai.Message, 0, len(saved))
	for _, m := range saved {
		history = append(history, ai.Message{Role: m.Role, Content: m.Content})
	}
	text, _, err := analysis.Chat(ctx, p, model, in, history)
	if err == nil && text == "" {
		err = errors.New("a IA devolveu uma resposta vazia")
	}
	if err != nil {
		// Sem resposta, a pergunta sai do histórico para a conversa não ficar com duas perguntas seguidas.
		_ = s.Store.DeleteChatMessage(ctx, q.ID)
		writeErr(w, aiStatus(err), err)
		return
	}
	a, err := s.Store.AddChatMessage(ctx, id, "assistant", text)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, []store.ChatMessage{q, a})
}

type aiSettings struct {
	Provider  string            `json:"provider"`
	Model     string            `json:"model"`
	Providers []ai.ProviderInfo `json:"providers,omitempty"`
	// GitHubToken diz só se GITHUB_TOKEN está definido (o valor nunca sai do backend).
	GitHubToken bool `json:"githubTokenConfigured"`
}

// GET /api/settings/ai
func (s *Server) getAISettings(w http.ResponseWriter, r *http.Request) {
	def := s.AI.Default()
	pid, err := s.Store.Setting(r.Context(), settingProvider, def.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	model := def.DefaultModel
	if p, err := s.AI.Get(pid); err == nil {
		model = p.Info().DefaultModel
	}
	model, err = s.Store.Setting(r.Context(), settingModel, model)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, aiSettings{Provider: pid, Model: model, Providers: s.AI.List(), GitHubToken: s.Code != nil && s.Code.Token != ""})
}

// PUT /api/settings/ai {"provider": "...", "model": "..."}
func (s *Server) putAISettings(w http.ResponseWriter, r *http.Request) {
	var body aiSettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("JSON inválido: %w", err))
		return
	}
	p, err := s.AI.Get(body.Provider)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	body.Model = strings.TrimSpace(body.Model)
	if body.Model == "" {
		body.Model = p.Info().DefaultModel
	}
	if err := s.Store.SetSetting(r.Context(), settingProvider, body.Provider); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.Store.SetSetting(r.Context(), settingModel, body.Model); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	body.Providers = s.AI.List()
	body.GitHubToken = s.Code != nil && s.Code.Token != ""
	writeJSON(w, http.StatusOK, body)
}
