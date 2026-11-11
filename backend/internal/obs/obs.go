// Package obs lê, no fim de uma execução, a telemetria da aplicação na stack de
// observabilidade opcional (grafana/otel-lgtm): métricas no Prometheus, traces no Tempo e
// profiling no Pyroscope. Tudo pela API HTTP de cada um, sem SDK.
package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// Client fala com a stack. Uma URL vazia desliga aquela parte.
type Client struct {
	Prometheus, Tempo, Pyroscope, Grafana string
	HTTP                                  *http.Client
	// Step é o passo das séries (as métricas chegam a cada 5 s).
	Step time.Duration
}

// FromEnv lê OBS_PROMETHEUS_URL, OBS_TEMPO_URL, OBS_PYROSCOPE_URL e OBS_GRAFANA_URL; nil se
// nenhuma das três primeiras estiver definida.
func FromEnv() *Client {
	c := &Client{
		Prometheus: strings.TrimRight(os.Getenv("OBS_PROMETHEUS_URL"), "/"),
		Tempo:      strings.TrimRight(os.Getenv("OBS_TEMPO_URL"), "/"),
		Pyroscope:  strings.TrimRight(os.Getenv("OBS_PYROSCOPE_URL"), "/"),
		Grafana:    strings.TrimRight(os.Getenv("OBS_GRAFANA_URL"), "/"),
	}
	if c.Prometheus == "" && c.Tempo == "" && c.Pyroscope == "" {
		return nil
	}
	return c
}

// Service é um Deployment observado (o alvo ou uma dependência).
type Service struct{ Namespace, Name string }

func (s Service) String() string { return s.Namespace + "/" + s.Name }

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) step() time.Duration {
	if c.Step > 0 {
		return c.Step
	}
	return 5 * time.Second
}

func (c *Client) get(ctx context.Context, base, path string, q url.Values, out any) error {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("%s respondeu %d: %s", path, resp.StatusCode, msg)
	}
	return json.Unmarshal(data, out)
}

// Collect junta métricas, traces e profiling dos serviços entre started e ended.
func (c *Client) Collect(ctx context.Context, svcs []Service, started, ended time.Time) *store.AppTelemetry {
	app := &store.AppTelemetry{Metrics: []store.MetricSeries{}, Traces: []store.TraceSummary{}, Profiles: []store.ProfileTop{},
		Available: map[string][]string{}, GrafanaURL: c.Grafana}
	for _, s := range svcs {
		if c.Prometheus != "" {
			c.metrics(ctx, app, s, started, ended)
		}
		if c.Tempo != "" {
			c.traces(ctx, app, s, started, ended)
		}
		if c.Pyroscope != "" {
			c.profiles(ctx, app, s, started, ended)
		}
	}
	return app
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// --- Prometheus ---

// Metric é uma série pedida ao Prometheus. $SEL vira o seletor do serviço. As alternativas
// (or) cobrem os nomes do OpenTelemetry Java agent e do Micrometer.
type Metric struct {
	Key, Label, Unit, Expr string
}

// Metrics são as séries que ajudam a explicar o comportamento numa falha.
var Metrics = []Metric{
	{"heap_mb", "Heap usado", "MB", `sum(jvm_memory_used_bytes{$SEL,jvm_memory_type="heap"}) / 1048576 or sum(jvm_memory_used_bytes{$SEL,area="heap"}) / 1048576`},
	{"gc_ms_s", "Pausa de GC", "ms/s", `sum(rate(jvm_gc_duration_seconds_sum{$SEL}[15s])) * 1000 or sum(rate(jvm_gc_pause_seconds_sum{$SEL}[15s])) * 1000`},
	{"threads", "Threads da JVM", "", `sum(jvm_thread_count{$SEL}) or sum(jvm_threads_live_threads{$SEL})`},
	{"http_rps", "Requisições recebidas", "req/s", `sum(rate(http_server_request_duration_seconds_count{$SEL}[15s])) or sum(rate(http_server_requests_seconds_count{$SEL}[15s]))`},
	{"http_p95_ms", "p95 das requisições recebidas", "ms", `histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket{$SEL}[15s]))) * 1000 or histogram_quantile(0.95, sum by (le) (rate(http_server_requests_seconds_bucket{$SEL}[15s]))) * 1000`},
	{"http_5xx_s", "Respostas 5xx", "/s", `sum(rate(http_server_request_duration_seconds_count{$SEL,http_response_status_code=~"5.."}[15s])) or sum(rate(http_server_requests_seconds_count{$SEL,status=~"5.."}[15s]))`},
	{"client_p95_ms", "p95 das chamadas HTTP de saída", "ms", `histogram_quantile(0.95, sum by (le) (rate(http_client_request_duration_seconds_bucket{$SEL}[15s]))) * 1000`},
	{"db_used", "Conexões do pool em uso", "", `sum(db_client_connections_usage{$SEL,state="used"}) or sum(db_client_connection_count{$SEL,db_client_connection_state="used"}) or sum(hikaricp_connections_active{$SEL})`},
	{"db_pending", "Esperando conexão do pool", "", `sum(db_client_connections_pending_requests{$SEL}) or sum(db_client_connection_pending_requests{$SEL}) or sum(hikaricp_connections_pending{$SEL})`},
	{"db_wait_p95_ms", "p95 da espera por conexão", "ms", `histogram_quantile(0.95, sum by (le) (rate(db_client_connections_wait_time_milliseconds_bucket{$SEL}[15s]))) or histogram_quantile(0.95, sum by (le) (rate(db_client_connection_wait_time_seconds_bucket{$SEL}[15s]))) * 1000`},
}

type promResp struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   json.RawMessage
}

func (c *Client) prom(ctx context.Context, path string, q url.Values, data any) error {
	var r promResp
	if err := c.get(ctx, c.Prometheus, path, q, &r); err != nil {
		return err
	}
	if r.Status != "success" {
		return fmt.Errorf("Prometheus: %s", r.Error)
	}
	return json.Unmarshal(r.Data, data)
}

// job acha o job do serviço: o Prometheus monta job = service.namespace/service.name.
func (c *Client) job(ctx context.Context, s Service, started, ended time.Time) (string, error) {
	var jobs []string
	q := url.Values{"start": {unix(started)}, "end": {unix(ended)}}
	if err := c.prom(ctx, "/api/v1/label/job/values", q, &jobs); err != nil {
		return "", err
	}
	for _, want := range []string{s.String(), s.Name} {
		for _, j := range jobs {
			if j == want {
				return j, nil
			}
		}
	}
	return "", nil
}

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func (c *Client) metrics(ctx context.Context, app *store.AppTelemetry, s Service, started, ended time.Time) {
	job, err := c.job(ctx, s, started, ended)
	if err != nil {
		app.Notes = append(app.Notes, fmt.Sprintf("métricas de %s: %v", s, err))
		return
	}
	if job == "" {
		app.Notes = append(app.Notes, fmt.Sprintf("%s não mandou métricas para o Prometheus (o alvo está instrumentado?)", s))
		return
	}
	sel := fmt.Sprintf(`job=%q`, job)
	var names []string
	q := url.Values{"match[]": {"{" + sel + "}"}, "start": {unix(started)}, "end": {unix(ended)}}
	if err := c.prom(ctx, "/api/v1/label/__name__/values", q, &names); err == nil {
		app.Available[s.String()] = names
	}
	for _, m := range Metrics {
		var data struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		}
		q := url.Values{
			"query": {strings.ReplaceAll(m.Expr, "$SEL", sel)},
			"start": {unix(started)}, "end": {unix(ended)},
			"step": {strconv.Itoa(int(c.step().Seconds()))},
		}
		if err := c.prom(ctx, "/api/v1/query_range", q, &data); err != nil {
			app.Notes = append(app.Notes, fmt.Sprintf("%s de %s: %v", m.Key, s, err))
			continue
		}
		if len(data.Result) == 0 {
			continue
		}
		var pts []store.PointValue
		for _, v := range data.Result[0].Values {
			ts, _ := v[0].(float64)
			str, _ := v[1].(string)
			f, err := strconv.ParseFloat(str, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				continue
			}
			pts = append(pts, store.PointValue{T: round(ts-float64(started.Unix()), 1), V: round(f, 2)})
		}
		if len(pts) > 0 {
			app.Metrics = append(app.Metrics, store.MetricSeries{Service: s.String(), Key: m.Key, Label: m.Label, Unit: m.Unit, Points: pts})
		}
	}
}

// --- Tempo ---

// SlowMs é a partir de quanto um trace conta como lento.
const SlowMs = 200

func (c *Client) traces(ctx context.Context, app *store.AppTelemetry, s Service, started, ended time.Time) {
	type hit struct {
		TraceID           string  `json:"traceID"`
		RootServiceName   string  `json:"rootServiceName"`
		RootTraceName     string  `json:"rootTraceName"`
		StartTimeUnixNano string  `json:"startTimeUnixNano"`
		DurationMs        float64 `json:"durationMs"`
	}
	search := func(traceQL string, limit int) ([]hit, error) {
		var r struct {
			Traces []hit `json:"traces"`
		}
		q := url.Values{"q": {traceQL}, "limit": {strconv.Itoa(limit)},
			"start": {strconv.FormatInt(started.Add(-5*time.Second).Unix(), 10)}, "end": {strconv.FormatInt(ended.Add(5*time.Second).Unix(), 10)}}
		err := c.get(ctx, c.Tempo, "/api/search", q, &r)
		return r.Traces, err
	}
	name := fmt.Sprintf("resource.service.name = %q", s.Name)
	groups := []struct {
		kind, q             string
		limit, keep, detail int
	}{
		{"erro", fmt.Sprintf("{ %s && status = error }", name), 50, 5, 2},
		{"lento", fmt.Sprintf("{ %s && duration > %dms }", name, SlowMs), 100, 5, 3},
	}
	seen := map[string]bool{}
	for _, g := range groups {
		hits, err := search(g.q, g.limit)
		if err != nil {
			app.Notes = append(app.Notes, fmt.Sprintf("traces de %s: %v", s, err))
			return
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].DurationMs > hits[j].DurationMs })
		n := 0
		for _, h := range hits {
			if seen[h.TraceID] || n >= g.keep {
				continue
			}
			seen[h.TraceID] = true
			ns, _ := strconv.ParseInt(h.StartTimeUnixNano, 10, 64)
			ts := store.TraceSummary{Service: s.String(), Kind: g.kind, TraceID: h.TraceID, Root: h.RootTraceName, DurationMs: round(h.DurationMs, 1),
				T: round(time.Unix(0, ns).Sub(started).Seconds(), 1)}
			if h.RootServiceName != "" && h.RootServiceName != s.Name {
				ts.Root = h.RootServiceName + ": " + h.RootTraceName
			}
			if n < g.detail {
				ts.Spans, _ = c.trace(ctx, h.TraceID)
			}
			app.Traces = append(app.Traces, ts)
			n++
		}
	}
}

type kv struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
		IntValue    any    `json:"intValue"`
	} `json:"value"`
}

type otlpSpan struct {
	Name              string `json:"name"`
	StartTimeUnixNano string `json:"startTimeUnixNano"`
	EndTimeUnixNano   string `json:"endTimeUnixNano"`
	Status            struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Attributes []kv `json:"attributes"`
	Events     []struct {
		Name       string `json:"name"`
		Attributes []kv   `json:"attributes"`
	} `json:"events"`
}

type otlpResource struct {
	Resource struct {
		Attributes []kv `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []otlpSpan `json:"spans"`
	} `json:"scopeSpans"`
	InstrumentationLibrarySpans []struct {
		Spans []otlpSpan `json:"spans"`
	} `json:"instrumentationLibrarySpans"`
}

func attr(attrs []kv, keys ...string) string {
	for _, k := range keys {
		for _, a := range attrs {
			if a.Key == k {
				if a.Value.StringValue != "" {
					return a.Value.StringValue
				}
				if a.Value.IntValue != nil {
					return fmt.Sprint(a.Value.IntValue)
				}
			}
		}
	}
	return ""
}

// MaxSpans é quantos spans de cada trace detalhado vão para a IA (os mais longos).
const MaxSpans = 8

// trace lê um trace e devolve os spans mais longos.
func (c *Client) trace(ctx context.Context, id string) ([]store.SpanSummary, error) {
	var r struct {
		Batches       []otlpResource `json:"batches"`
		ResourceSpans []otlpResource `json:"resourceSpans"`
		Trace         *struct {
			ResourceSpans []otlpResource `json:"resourceSpans"`
		} `json:"trace"`
	}
	if err := c.get(ctx, c.Tempo, "/api/traces/"+url.PathEscape(id), nil, &r); err != nil {
		return nil, err
	}
	res := append(r.Batches, r.ResourceSpans...)
	if r.Trace != nil {
		res = append(res, r.Trace.ResourceSpans...)
	}
	var out []store.SpanSummary
	for _, b := range res {
		svc := attr(b.Resource.Attributes, "service.name")
		var spans []otlpSpan
		for _, ss := range b.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
		for _, ss := range b.InstrumentationLibrarySpans {
			spans = append(spans, ss.Spans...)
		}
		for _, sp := range spans {
			st, _ := strconv.ParseInt(sp.StartTimeUnixNano, 10, 64)
			en, _ := strconv.ParseInt(sp.EndTimeUnixNano, 10, 64)
			code := fmt.Sprint(sp.Status.Code)
			isErr := code == "2" || code == "STATUS_CODE_ERROR"
			detail := attr(sp.Attributes, "db.query.text", "db.statement", "url.full", "http.url", "server.address")
			if isErr {
				msg := sp.Status.Message
				for _, e := range sp.Events {
					if e.Name == "exception" {
						if m := attr(e.Attributes, "exception.type"); m != "" {
							msg = m + ": " + attr(e.Attributes, "exception.message")
						}
					}
				}
				if msg != "" {
					detail = strings.TrimSpace(detail + " " + msg)
				}
			}
			if len(detail) > 200 {
				detail = detail[:200] + "…"
			}
			out = append(out, store.SpanSummary{Service: svc, Name: sp.Name, DurationMs: round(float64(en-st)/1e6, 1), Error: isErr, Detail: detail})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Error != out[j].Error {
			return out[i].Error
		}
		return out[i].DurationMs > out[j].DurationMs
	})
	if len(out) > MaxSpans {
		out = out[:MaxSpans]
	}
	return out, nil
}

// --- Pyroscope ---

// ProfileTypes são os perfis lidos do Pyroscope Java agent (JFR).
var ProfileTypes = []struct{ Label, ID string }{
	{"cpu", "process_cpu:cpu:nanoseconds:cpu:nanoseconds"},
	{"alocação", "memory:alloc_in_new_tlab_bytes:bytes:space:bytes"},
}

// TopFunctions é quantas funções de cada perfil vão para a IA.
const TopFunctions = 12

func (c *Client) profiles(ctx context.Context, app *store.AppTelemetry, s Service, started, ended time.Time) {
	for _, pt := range ProfileTypes {
		var r struct {
			Flamebearer struct {
				Names    []string  `json:"names"`
				Levels   [][]int64 `json:"levels"`
				NumTicks int64     `json:"numTicks"`
			} `json:"flamebearer"`
		}
		q := url.Values{"query": {fmt.Sprintf("%s{service_name=%q}", pt.ID, s.Name)}, "from": {unix(started)}, "until": {unix(ended)}, "format": {"json"}}
		if err := c.get(ctx, c.Pyroscope, "/pyroscope/render", q, &r); err != nil {
			app.Notes = append(app.Notes, fmt.Sprintf("profiling (%s) de %s: %v", pt.Label, s, err))
			return
		}
		top := TopSelf(r.Flamebearer.Names, r.Flamebearer.Levels, r.Flamebearer.NumTicks, TopFunctions)
		if len(top) > 0 {
			app.Profiles = append(app.Profiles, store.ProfileTop{Service: s.String(), Type: pt.Label, Functions: top})
		}
	}
}

// TopSelf soma o tempo próprio (self) de cada função num flamebearer do Pyroscope. Cada nível
// tem grupos de 4 números: deslocamento, total, self e índice do nome.
func TopSelf(names []string, levels [][]int64, total int64, n int) []store.ProfileEntry {
	if total <= 0 {
		return nil
	}
	self := map[string]int64{}
	for _, lv := range levels {
		for i := 0; i+3 < len(lv); i += 4 {
			idx := lv[i+3]
			if idx < 0 || int(idx) >= len(names) || lv[i+2] == 0 {
				continue
			}
			self[names[idx]] += lv[i+2]
		}
	}
	out := make([]store.ProfileEntry, 0, len(self))
	for name, v := range self {
		out = append(out, store.ProfileEntry{Name: name, SelfPct: round(float64(v)/float64(total)*100, 1)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SelfPct != out[j].SelfPct {
			return out[i].SelfPct > out[j].SelfPct
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}
