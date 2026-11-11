// Package store guarda alvos, cenários e execuções.
// No MVP a implementação será SQLite; a interface permite trocar por PostgreSQL.
package store

import (
	"context"
	"errors"
	"sync"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

var ErrNotFound = errors.New("não encontrado")

type Store interface {
	SaveScenario(ctx context.Context, s scenario.Scenario) error
	GetScenario(ctx context.Context, id string) (scenario.Scenario, error)
	ListScenarios(ctx context.Context) ([]scenario.Scenario, error)
}

// Memory é uma implementação em memória, usada até o SQLite entrar.
type Memory struct {
	mu        sync.RWMutex
	scenarios map[string]scenario.Scenario
}

func NewMemory() *Memory { return &Memory{scenarios: map[string]scenario.Scenario{}} }

func (m *Memory) SaveScenario(_ context.Context, s scenario.Scenario) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scenarios[s.ID] = s
	return nil
}

func (m *Memory) GetScenario(_ context.Context, id string) (scenario.Scenario, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.scenarios[id]
	if !ok {
		return scenario.Scenario{}, ErrNotFound
	}
	return s, nil
}

func (m *Memory) ListScenarios(_ context.Context) ([]scenario.Scenario, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]scenario.Scenario, 0, len(m.scenarios))
	for _, s := range m.scenarios {
		out = append(out, s)
	}
	return out, nil
}
