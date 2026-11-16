// Package load gera o script k6 a partir do cenário e agrega as métricas que o k6 emite.
package load

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/scenario"
)

// MaxMeasuredEndpoints limita os endpoints com métricas próprias: cada um acrescenta três
// métricas a todo resumo que o k6 publica por segundo. Os demais entram só nos totais.
const MaxMeasuredEndpoints = 20

// Nomes das métricas que o script cria e o leitor do dashboard procura (ver summary.go).
const (
	metricSteadyDuration = "steady_duration"
	metricSteadyReqs     = "steady_reqs"
	metricSteadyErrs     = "steady_errs"
	metricDropped        = "dropped_iterations" // do próprio k6, só no modelo aberto
)

func endpointMetric(i int, what string) string { return fmt.Sprintf("ep_%d_%s", i, what) }

// Script gera um script k6 (JavaScript) para o cenário.
//
// As métricas não saem uma linha por requisição (isso passa de 200 mil linhas/s numa API rápida).
// O k6 roda com o dashboard embutido, que agrega por segundo, e o backend lê esses resumos
// (ver dashboard.go). O resumo só traz métricas sem etiqueta, então o que precisa ser separado
// vira uma métrica com nome próprio:
//   - status_*: os códigos HTTP;
//   - steady_*: as requisições iniciadas depois do aquecimento (a janela estável);
//   - ep_<i>_*: o mesmo, por endpoint, na ordem do cenário.
//
// No modelo aberto (Profile.Rate > 0) o executor é o ramping-arrival-rate: a taxa de chegada
// não depende do tempo de resposta. Os VUs são todos pré-alocados, porque criar VUs no meio do
// teste custa CPU do gerador e distorce a medição.
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
	options := map[string]any{"discardResponseBodies": true}
	if s.Profile.OpenModel() {
		options["scenarios"] = map[string]any{"load": map[string]any{
			"executor":        "ramping-arrival-rate",
			"startRate":       0,
			"timeUnit":        "1s",
			"preAllocatedVUs": s.Profile.VUs,
			"maxVUs":          s.Profile.VUs,
			"stages":          ks,
		}}
	} else {
		options["stages"] = ks
	}
	optionsJSON, err := json.Marshal(options)
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
import exec from 'k6/execution';
import { Counter, Trend } from 'k6/metrics';

const status2xx = new Counter('status_2xx');
const status3xx = new Counter('status_3xx');
const status4xx = new Counter('status_4xx');
const status5xx = new Counter('status_5xx');
const statusNone = new Counter('status_none');

const BASE_URL = %q;
const ENDPOINTS = %s;
const TOTAL_WEIGHT = ENDPOINTS.reduce((acc, e) => acc + e.weight, 0);
const WARMUP_MS = %d;

// Janela estável: só as requisições iniciadas depois do aquecimento.
const steadyDuration = new Trend('steady_duration', true);
const steadyReqs = new Counter('steady_reqs');
const steadyErrs = new Counter('steady_errs');
const MEASURED = Math.min(ENDPOINTS.length, %d);
const epDuration = [], epReqs = [], epErrs = [];
for (let i = 0; i < MEASURED; i++) {
  epDuration.push(new Trend('ep_' + i + '_duration', true));
  epReqs.push(new Counter('ep_' + i + '_reqs'));
  epErrs.push(new Counter('ep_' + i + '_errs'));
}

export const options = %s;

function pick() {
  let r = Math.random() * TOTAL_WEIGHT;
  for (let i = 0; i < ENDPOINTS.length; i++) {
    r -= ENDPOINTS[i].weight;
    if (r <= 0) return i;
  }
  return 0;
}

export default function () {
  const i = pick();
  const e = ENDPOINTS[i];
  const steady = exec.instance.currentTestRunDuration >= WARMUP_MS;
  const res = http.request(e.method, BASE_URL + e.path, e.body || null, { headers: e.headers || {}, timeout: '%ds' });
  const s = res.status;
  if (s >= 500) status5xx.add(1);
  else if (s >= 400) status4xx.add(1);
  else if (s >= 300) status3xx.add(1);
  else if (s >= 200) status2xx.add(1);
  else statusNone.add(1);
  if (!steady) return;
  // Mesmo critério do http_req_failed do k6: sem resposta ou código fora de 200-399.
  const failed = s < 200 || s >= 400;
  steadyDuration.add(res.timings.duration);
  steadyReqs.add(1);
  if (failed) steadyErrs.add(1);
  if (i < MEASURED) {
    epDuration[i].add(res.timings.duration);
    epReqs[i].add(1);
    if (failed) epErrs[i].add(1);
  }
}
`, strings.TrimRight(s.Target.BaseURL, "/"), endpointsJSON, s.Profile.WarmupSeconds*1000, MaxMeasuredEndpoints, optionsJSON, s.RequestTimeout())
	return b.String(), nil
}
