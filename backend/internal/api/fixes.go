package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/analysis"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/auth"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/fix"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/runner"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

func (s *Server) fixesOn(w http.ResponseWriter) bool {
	if s.Fixes == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("as correções automáticas não estão disponíveis neste ambiente"))
		return false
	}
	return true
}

// who devolve o usuário logado ("" com o login desligado).
func who(r *http.Request) string {
	u, _ := auth.UserFrom(r.Context())
	return u.Username
}

// POST /api/runs/{id}/fixes {"index": 0}: registra a tentativa de transformar a correção
// "index" do relatório da IA num PR. Nada roda aqui: a tentativa fica esperando aprovação.
func (s *Server) createFix(w http.ResponseWriter, r *http.Request) {
	if !s.fixesOn(w) {
		return
	}
	runID := chi.URLParam(r, "id")
	var body struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("JSON inválido: %w", err))
		return
	}
	run, err := s.Store.GetRun(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var rep analysis.Report
	if err := s.Store.Analysis(r.Context(), runID, &rep); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusConflict, errors.New("esta execução ainda não tem análise da IA"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if body.Index < 0 || body.Index >= len(rep.Content.Fixes) {
		writeErr(w, http.StatusBadRequest, errors.New("essa correção não existe no relatório"))
		return
	}
	if rep.Code == nil {
		writeErr(w, http.StatusConflict, errors.New("a análise foi feita sem o código da aplicação; configure o repositório no cenário e refaça a análise"))
		return
	}
	f := rep.Content.Fixes[body.Index]
	if f.Applies != nil && !*f.Applies {
		writeErr(w, http.StatusConflict, fmt.Errorf("o patch desta correção não se aplica ao código analisado (%s); refaça a análise", f.ApplyError))
		return
	}
	// Como construir e testar vem do cenário atual: pode ter sido preenchido depois da execução.
	code := run.Scenario.Code
	if sc, err := s.Store.GetScenario(r.Context(), run.ScenarioID); err == nil && sc.Code != nil {
		code = sc.Code
	}
	a := fix.Attempt{
		ID: runner.NewID(), RunID: run.ID, ScenarioID: run.ScenarioID, FixIndex: body.Index,
		Title: f.Title, Detail: f.Detail, Metric: f.Metric, Expected: f.Expected, Proposed: f.Diff,
		Repo: rep.Code.Repo, BaseBranch: rep.Code.Branch, Commit: rep.Code.Commit, Path: rep.Code.Path,
		Model: rep.Usage.Model, CreatedBy: who(r), CreatedAt: time.Now().UTC(),
	}
	if code != nil {
		a.TestImage, a.TestCommand = code.TestImage, code.TestCommand
	}
	a, err = s.Fixes.Create(r.Context(), a)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// GET /api/fixes?runId=
func (s *Server) listFixes(w http.ResponseWriter, r *http.Request) {
	if s.Fixes == nil {
		writeJSON(w, http.StatusOK, []fix.Attempt{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	list, err := s.Fixes.List(r.Context(), r.URL.Query().Get("runId"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// GET /api/fixes/{id}: a tentativa e, enquanto espera aprovação, o que falta para poder rodar.
func (s *Server) getFix(w http.ResponseWriter, r *http.Request) {
	if !s.fixesOn(w) {
		return
	}
	a, err := s.Fixes.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := struct {
		fix.Attempt
		// Blocked diz por que a tentativa não pode ser aprovada agora ("" = pode).
		Blocked string `json:"blocked,omitempty"`
		// CanDecide diz se quem está logado pode aprovar ou recusar.
		CanDecide bool `json:"canDecide"`
	}{Attempt: a, CanDecide: s.canDecide(r)}
	if a.Status == fix.AwaitingApproval {
		out.Blocked = s.Fixes.Ready()
	}
	writeJSON(w, http.StatusOK, out)
}

// canDecide: com o login ligado, só o administrador aprova ou recusa uma tentativa. Quem
// cria cenários e pede a correção não é, por isso, quem autoriza código gerado por IA a rodar
// e a virar PR. Com o login desligado (modo local) há uma pessoa só.
func (s *Server) canDecide(r *http.Request) bool {
	if s.Auth == nil || !s.Auth.Enabled {
		return true
	}
	u, ok := auth.UserFrom(r.Context())
	return ok && u.Admin
}

func (s *Server) decideFix(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.fixesOn(w) {
			return
		}
		if !s.canDecide(r) {
			writeErr(w, http.StatusForbidden, errors.New("só o administrador aprova ou recusa uma tentativa de correção"))
			return
		}
		id := chi.URLParam(r, "id")
		var a fix.Attempt
		var err error
		if approve {
			a, err = s.Fixes.Approve(r.Context(), id, who(r))
		} else {
			a, err = s.Fixes.Reject(r.Context(), id, who(r))
		}
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeStoreErr(w, err)
		case errors.Is(err, fix.ErrState):
			writeErr(w, http.StatusConflict, err)
		case err != nil:
			writeErr(w, http.StatusPreconditionFailed, err)
		default:
			writeJSON(w, http.StatusOK, a)
		}
	}
}
