package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/experiment"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// POST /api/experiments: cria e dispara um experimento A/B (ou A/A, sem "b").
func (s *Server) createExperiment(w http.ResponseWriter, r *http.Request) {
	var spec experiment.Spec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("JSON inválido: %w", err))
		return
	}
	sc, err := s.Store.GetScenario(r.Context(), spec.ScenarioID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, errors.New("escolha o cenário do experimento"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// As travas do ambiente valem para os dois lados: B é um alvo como outro qualquer.
	if err := s.checkScenario(sc); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if spec.B != nil {
		b := sc
		b.Target = *spec.B
		if err := b.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("alvo B: %w", err))
			return
		}
		if err := s.checkScenario(b); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("alvo B: %w", err))
			return
		}
	}
	e, err := s.Experiments.Start(r.Context(), spec, sc)
	if errors.Is(err, experiment.ErrBusy) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

// GET /api/experiments?scenarioId=
func (s *Server) listExperiments(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	list, err := s.Experiments.List(r.Context(), r.URL.Query().Get("scenarioId"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// GET /api/experiments/{id}
func (s *Server) getExperiment(w http.ResponseWriter, r *http.Request) {
	e, err := s.Experiments.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// POST /api/experiments/{id}/abort
func (s *Server) abortExperiment(w http.ResponseWriter, r *http.Request) {
	if err := s.Experiments.Abort(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusConflict, errors.New("o experimento não está em andamento"))
			return
		}
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
