// Package load gera o script k6 a partir do cenário e agrega as métricas que o k6 emite.
package load

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// Script gera um script k6 (JavaScript) para o cenário.
//
// As métricas não saem uma linha por requisição (isso passa de 200 mil linhas/s numa API rápida).
// O k6 roda com o dashboard embutido, que agrega por segundo, e o backend lê esses resumos
// (ver dashboard.go). Os contadores status_* existem porque o resumo não traz códigos HTTP.
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
import { Counter } from 'k6/metrics';

const status2xx = new Counter('status_2xx');
const status3xx = new Counter('status_3xx');
const status4xx = new Counter('status_4xx');
const status5xx = new Counter('status_5xx');
const statusNone = new Counter('status_none');

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
  const res = http.request(e.method, BASE_URL + e.path, e.body || null, { headers: e.headers || {}, timeout: '%ds' });
  const s = res.status;
  if (s >= 500) status5xx.add(1);
  else if (s >= 400) status4xx.add(1);
  else if (s >= 300) status3xx.add(1);
  else if (s >= 200) status2xx.add(1);
  else statusNone.add(1);
}
`, strings.TrimRight(s.Target.BaseURL, "/"), endpointsJSON, stagesJSON, s.RequestTimeout())
	return b.String(), nil
}
