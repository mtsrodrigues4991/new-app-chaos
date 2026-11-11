// Package api expõe a API REST (e, depois, o WebSocket) consumida pelo frontend.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/dockerx"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

type Server struct {
	Docker *dockerx.Client // pode ser nil se o Docker não estiver acessível
	Store  store.Store
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)

	r.Get("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "docker": s.Docker != nil})
	})
	r.Get("/api/containers", s.listContainers)
	r.Get("/api/scenarios", s.listScenarios)
	r.Post("/api/scenarios", s.saveScenario)
	return r
}

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	if s.Docker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Docker não acessível"})
		return
	}
	list, err := s.Docker.ListRunning(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) listScenarios(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListScenarios(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) saveScenario(w http.ResponseWriter, r *http.Request) {
	var sc scenario.Scenario
	if err := json.NewDecoder(r.Body).Decode(&sc); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON inválido: " + err.Error()})
		return
	}
	if sc.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id é obrigatório"})
		return
	}
	if err := s.Store.SaveScenario(r.Context(), sc); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
