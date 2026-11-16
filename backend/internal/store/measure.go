package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/load"
)

const measureSchema = `
CREATE TABLE IF NOT EXISTS run_versions (
  run_id TEXT PRIMARY KEY,
  data   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_summaries (
  run_id TEXT PRIMARY KEY,
  data   TEXT NOT NULL
);
`

// Papéis de um componente testado.
const (
	RoleTarget     = "target"
	RoleDependency = "dependency"
)

// Version é a versão de um componente (o alvo ou uma dependência) no início da execução:
// sem isso não dá para dizer qual build produziu qual número.
type Version struct {
	Role string `json:"role"` // RoleTarget ou RoleDependency
	Name string `json:"name"` // container, ou namespace/deployment
	// Commit é o commit do código que gerou a imagem, quando ela o declara (rótulo OCI
	// org.opencontainers.image.revision no Docker; anotação de mesmo nome no pod, no
	// Kubernetes). Vazio = não declarado: o app não supõe.
	Commit string `json:"commit,omitempty"`
	// Images são as imagens em uso. Mais de uma entrada para o mesmo container significa pods
	// com versões diferentes (troca de versão em andamento).
	Images []Image `json:"images"`
	// Mixed indica pods com imagens ou commits diferentes: o resultado mistura duas versões.
	Mixed bool `json:"mixed,omitempty"`
}

// Image é uma imagem em uso por um container.
type Image struct {
	Container string `json:"container,omitempty"` // nome do container no pod (Kubernetes)
	Image     string `json:"image"`               // como foi pedida (nome:tag)
	// ID identifica o conteúdo: o digest do registry (nome@sha256:...) ou, para imagens que só
	// existem localmente, o id da imagem (sha256:...).
	ID   string `json:"id,omitempty"`
	Pods int    `json:"pods,omitempty"` // quantos pods usam esta imagem (Kubernetes)
}

func (s *Store) saveJSON(ctx context.Context, table, runID string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO `+table+` (run_id, data) VALUES (?, ?)`, runID, string(data))
	return err
}

// loadJSON devolve false quando a execução não tem o registro (execuções antigas).
func (s *Store) loadJSON(ctx context.Context, table, runID string, v any) (bool, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM `+table+` WHERE run_id = ?`, runID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(data), v)
}

// SaveVersions grava as versões do alvo e das dependências de uma execução.
func (s *Store) SaveVersions(ctx context.Context, runID string, vs []Version) error {
	return s.saveJSON(ctx, "run_versions", runID, vs)
}

// Versions devolve as versões registradas (nil em execuções anteriores ao registro).
func (s *Store) Versions(ctx context.Context, runID string) ([]Version, error) {
	var vs []Version
	_, err := s.loadJSON(ctx, "run_versions", runID, &vs)
	return vs, err
}

// SaveSummary grava o resumo da execução inteira.
func (s *Store) SaveSummary(ctx context.Context, runID string, sum load.Summary) error {
	return s.saveJSON(ctx, "run_summaries", runID, sum)
}

// Summary devolve o resumo da execução (nil sem carga ou em execuções anteriores ao resumo).
func (s *Store) Summary(ctx context.Context, runID string) (*load.Summary, error) {
	var sum load.Summary
	ok, err := s.loadJSON(ctx, "run_summaries", runID, &sum)
	if !ok || err != nil {
		return nil, err
	}
	return &sum, nil
}

// TargetCommit devolve o commit declarado pelo alvo ("" se não houver ou se os pods divergem).
func TargetCommit(vs []Version) string {
	for _, v := range vs {
		if v.Role == RoleTarget && !v.Mixed {
			return v.Commit
		}
	}
	return ""
}
