// Package load gera o script k6 a partir do cenário e agrega as métricas que o k6 emite.
package load

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Script gera um script k6 (JavaScript) para o cenário.
// O k6 roda num container próprio com --out json=/dev/stdout e o backend lê as métricas em streaming.
func Script(s scenario.Scenario) (string, error) {
	stages, err := s.Profile.Stages()
	if err != nil {
		return "", err
	}
	type k6Stage struct {
		Duration string `json:"duration"`
		Target   int    `json:"target"`
	}
	ks := make([]k6Stage, 0, len(stages))
	for _, st := range stages {
		ks = append(ks, k6Stage{Duration: fmt.Sprintf("%ds", st.DurationSeconds), Target: st.Target})
	}
	stagesJSON, err := json.Marshal(ks)
	if err != nil {
		return "", err
	}
	eps := make([]scenario.Endpoint, len(s.Endpoints))
	for i, e := range s.Endpoints {
		e.Method = strings.ToUpper(e.Method)
		if e.Weight < 1 {
			e.Weight = 1
		}
		eps[i] = e
	}
	endpointsJSON, err := json.Marshal(eps)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, `import http from 'k6/http';

const BASE_URL = %q;
const ENDPOINTS = %s;

export const options = {
  stages: %s,
  discardResponseBodies: true,
};

function pick() {
  const total = ENDPOINTS.reduce((acc, e) => acc + e.weight, 0);
  let r = Math.random() * total;
  for (const e of ENDPOINTS) {
    r -= e.weight;
    if (r <= 0) return e;
  }
  return ENDPOINTS[0];
}

export default function () {
  const e = pick();
  http.request(e.method, BASE_URL + e.path, e.body || null, { headers: e.headers || {}, timeout: '10s' });
}
`, strings.TrimRight(s.Target.BaseURL, "/"), endpointsJSON, stagesJSON)
	return b.String(), nil
}
