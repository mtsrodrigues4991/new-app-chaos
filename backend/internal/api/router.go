// Package api expõe a API REST e o stream ao vivo (Server-Sent Events) consumidos pelo frontend.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ai"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/analysis"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/coderepo"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/kube"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/runner"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/verdict"
)

type Server struct {
	Docker *dockerx.Client // nil se o Docker não estiver acessível
	Kube   *kube.Client    // nil fora de um cluster Kubernetes
	Store  *store.Store
	Runner *runner.Manager
	AI     *ai.Registry
	Code   *coderepo.Fetcher // baixa o código da aplicação alvo para a IA
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		h := map[string]any{"status": "ok", "docker": s.Docker != nil, "kubernetes": s.Kube != nil, "observability": s.Runner != nil && s.Runner.Obs != nil}
		if s.Kube != nil {
			h["namespace"] = s.Kube.Namespace()
		}
		writeJSON(w, http.StatusOK, h)
	})
	r.Get("/api/containers", s.listContainers)
	r.Get("/api/deployments", s.listDeployments)

	r.Get("/api/scenarios", s.listScenarios)
	r.Post("/api/scenarios", s.createScenario)
	r.Get("/api/scenarios/{id}", s.getScenario)
	r.Put("/api/scenarios/{id}", s.updateScenario)
	r.Delete("/api/scenarios/{id}", s.deleteScenario)
	r.Post("/api/scenarios/{id}/runs", s.startRun)
	r.Put("/api/scenarios/{id}/reference", s.setReference)

	r.Get("/api/runs", s.listRuns)
	r.Get("/api/runs/{id}", s.getRun)
	r.Get("/api/runs/{id}/stream", s.streamRun)
	r.Get("/api/runs/{id}/diagnostics", s.getDiagnostics)
	r.Post("/api/runs/{id}/abort", s.abortRun)
	r.Post("/api/runs/{id}/analysis", s.analyzeRun)
	r.Get("/api/runs/{id}/chat", s.getChat)
	r.Post("/api/runs/{id}/chat", s.postChat)
	r.Delete("/api/runs/{id}/chat", s.clearChat)

	r.Get("/api/settings/ai", s.getAISettings)
	r.Put("/api/settings/ai", s.putAISettings)
	return r
}

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	if s.Docker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("Docker não acessível pelo backend"))
		return
	}
	list, err := s.Docker.ListRunning(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// --- cenários ---

func (s *Server) listScenarios(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListScenarios(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getScenario(w http.ResponseWriter, r *http.Request) {
	sc, err := s.Store.GetScenario(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func decodeScenario(r *http.Request) (scenario.Scenario, error) {
	var sc scenario.Scenario
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		return sc, fmt.Errorf("JSON inválido: %w", err)
	}
	if sc.Mode == "" {
		sc.Mode = scenario.ModeLoad
	}
	if sc.Code != nil {
		if strings.TrimSpace(sc.Code.Repo) == "" {
			sc.Code = nil
		} else {
			repo, err := coderepo.NormalizeRepo(sc.Code.Repo)
			if err != nil {
				return sc, err
			}
			sc.Code.Repo = repo
			sc.Code.Branch = strings.TrimSpace(sc.Code.Branch)
			if sc.Code.Branch == "" {
				sc.Code.Branch = "main"
			}
			sc.Code.Path = strings.Trim(strings.TrimSpace(sc.Code.Path), "/")
		}
	}
	return sc, sc.Validate()
}

func (s *Server) createScenario(w http.ResponseWriter, r *http.Request) {
	sc, err := decodeScenario(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sc.ID = runner.NewID()
	if err := s.Store.SaveScenario(r.Context(), sc); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Server) updateScenario(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.Store.GetScenario(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	sc, err := decodeScenario(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sc.ID = id
	if err := s.Store.SaveScenario(r.Context(), sc); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) deleteScenario(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteScenario(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- execuções ---

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	sc, err := s.Store.GetScenario(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	run, err := s.Runner.Start(r.Context(), sc)
	if errors.Is(err, runner.ErrBusy) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	list, err := s.Store.ListRuns(r.Context(), r.URL.Query().Get("scenarioId"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// GET /api/runs/{id}/diagnostics: logs e eventos coletados ({} se não houver).
func (s *Server) getDiagnostics(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Diagnostics(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if d == nil {
		d = &store.Diagnostics{Logs: []store.PodLog{}, Events: []store.KubeEvent{}}
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	d, err := s.runData(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	refID, err := s.Store.Baseline(r.Context(), d.Run.ScenarioID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := struct {
		store.Run
		Windows        []load.Window      `json:"windows"`
		FaultEvents    []store.FaultEvent `json:"faultEvents"`
		Verdict        *verdict.Verdict   `json:"verdict,omitempty"` // só depois que a execução termina
		ReferenceRunID string             `json:"referenceRunId,omitempty"`
		Analysis       *analysis.Report   `json:"analysis,omitempty"`
	}{Run: d.Run, Windows: d.Windows, FaultEvents: d.Events, ReferenceRunID: refID}
	if finished(d.Run) {
		out.Verdict = &d.Verdict
		var rep analysis.Report
		if err := s.Store.Analysis(r.Context(), d.Run.ID, &rep); err == nil {
			out.Analysis = &rep
		} else if !errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) abortRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Runner.Abort(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusConflict, errors.New("a execução não está em andamento"))
			return
		}
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// streamRun envia os eventos ao vivo via Server-Sent Events. Se a execução já terminou,
// envia só o status final e fecha: o frontend busca as janelas salvas em GET /api/runs/{id}.
func (s *Server) streamRun(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming não suportado"))
		return
	}
	id := chi.URLParam(r, "id")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // desliga o buffer do nginx
	send := func(ev runner.Event) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	ch, unsubscribe, active := s.Runner.Subscribe(id)
	if !active {
		run, err := s.Store.GetRun(r.Context(), id)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		send(runner.Event{Type: "status", Status: run.Status, Error: run.Error})
		fmt.Fprint(w, "event: end\ndata: {}\n\n")
		flusher.Flush()
		return
	}
	defer unsubscribe()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				fmt.Fprint(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			send(ev)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeErr(w, http.StatusInternalServerError, err)
}
