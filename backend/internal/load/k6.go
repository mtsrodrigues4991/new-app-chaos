// Package load gera o script k6 a partir do cenário.
package load

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Script gera um script k6 (JavaScript) para o cenário.
// O k6 roda num container próprio com --out json=... para o backend ler as métricas em streaming.
func Script(baseURL string, s scenario.Scenario) (string, error) {
	type stage struct {
		Duration string `json:"duration"`
		Target   int    `json:"target"`
	}
	stages := make([]stage, 0, len(s.Stages))
	for _, st := range s.Stages {
		stages = append(stages, stage{Duration: fmt.Sprintf("%ds", int(st.Duration.Seconds())), Target: st.Target})
	}
	stagesJSON, err := json.Marshal(stages)
	if err != nil {
		return "", err
	}
	endpointsJSON, err := json.Marshal(s.Endpoints)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, `import http from 'k6/http';

const BASE_URL = %q;
const ENDPOINTS = %s;

export const options = { stages: %s };

function pick() {
  const total = ENDPOINTS.reduce((acc, e) => acc + (e.weight || 1), 0);
  let r = Math.random() * total;
  for (const e of ENDPOINTS) {
    r -= e.weight || 1;
    if (r <= 0) return e;
  }
  return ENDPOINTS[0];
}

export default function () {
  const e = pick();
  http.request(e.method, BASE_URL + e.path, e.body || null, { headers: e.headers || {} });
}
`, baseURL, endpointsJSON, stagesJSON)
	return b.String(), nil
}
