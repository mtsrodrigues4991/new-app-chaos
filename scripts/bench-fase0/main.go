// Benchmark da Fase 0 (docs/analise-evolucao.md): a carga entregue muda quando a latência muda?
//
// Roda o mesmo cenário com 0 e com 200 ms de latência injetada, em três braços:
//
//	antes     backend antigo, carga em VUs (modelo fechado, o único que existia)
//	controle  backend novo, a mesma carga em VUs (a medição nova não pode mudar o resultado)
//	taxa      backend novo, carga por taxa de chegada (modelo aberto)
//
// Segue a metodologia da seção 5.2 da análise: a unidade é a execução (não o segundo), cinco
// repetições por condição, condições intercaladas, aquecimento descartado e decisão pelo
// intervalo de 95% (bootstrap) da diferença das medianas. Quem sobe o ambiente é o
// scripts/bench-fase0.sh.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

var (
	before   = flag.String("before", "http://nacbench-before:8080", "API do backend antigo")
	after    = flag.String("after", "http://nacbench-after:8080", "API do backend novo")
	target   = flag.String("target", "nacbench-demo-api", "container alvo")
	reps     = flag.Int("reps", 5, "repetições por condição")
	duration = flag.Int("duration", 60, "duração de cada execução (s)")
	vus      = flag.Int("vus", 20, "VUs do modelo fechado")
	rate     = flag.Int("rate", 250, "taxa de chegada do modelo aberto (req/s)")
	preVUs   = flag.Int("pre-vus", 150, "VUs pré-alocados do modelo aberto")
	latency  = flag.Int("latency", 200, "latência injetada (ms)")
	warmup   = flag.Int("warmup", 20, "aquecimento descartado (s)")
	out      = flag.String("out", "/out", "pasta dos resultados")
)

type obj = map[string]any

type arm struct {
	Name, API string
	Open      bool
}

// Result é o que se guarda de cada execução.
type Result struct {
	Arm    string `json:"arm"`
	Cond   string `json:"cond"` // lat0 ou lat200
	Rep    int    `json:"rep"`
	Order  int    `json:"order"` // posição na sequência de execuções
	RunID  string `json:"runId"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Medidos do mesmo jeito nos três braços, pelas janelas de 1 s entre o fim do aquecimento
	// e o início da descida do perfil (o backend antigo não tem resumo da execução).
	WinRPS    float64 `json:"winRps"`
	WinP50    float64 `json:"winP50Ms"`
	WinP95    float64 `json:"winP95Ms"`
	WinErrPct float64 `json:"winErrPct"`
	WinCPU    float64 `json:"winCpuPct"`
	FaultFrom float64 `json:"faultAppliedAt,omitempty"`
	// Só no backend novo.
	Summary  json.RawMessage `json:"summary,omitempty"`
	Versions json.RawMessage `json:"versions,omitempty"`
}

func call(method, url string, body, into any) error {
	var rd *bytes.Reader = bytes.NewReader(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, url, resp.StatusCode, raw)
	}
	if into != nil {
		return json.Unmarshal(raw, into)
	}
	return nil
}

func scenario(a arm, withLatency bool) obj {
	profile := obj{"type": "plateau", "vus": *vus, "durationSeconds": *duration}
	if a.Open {
		profile = obj{"type": "plateau", "vus": *preVUs, "rate": *rate, "durationSeconds": *duration, "warmupSeconds": *warmup}
	}
	sc := obj{
		"name":   fmt.Sprintf("bench-%s-lat%v", a.Name, withLatency),
		"target": obj{"platform": "docker", "container": *target, "baseUrl": "http://" + *target + ":8080"},
		"mode":   "load",
		"endpoints": []obj{
			{"method": "GET", "path": "/items", "weight": 9},
			{"method": "GET", "path": "/compute", "weight": 1},
		},
		"profile": profile,
	}
	if withLatency {
		sc["mode"] = "both"
		// A falha entra antes do fim do aquecimento e sai depois do fim da janela medida.
		sc["faults"] = []obj{{"id": "lat", "type": "latency", "startSeconds": 8, "durationSeconds": *duration - 10,
			"params": obj{"latency": fmt.Sprint(*latency)}}}
	}
	return sc
}

type window struct {
	T         int      `json:"t"`
	P50       *float64 `json:"p50"`
	P95       *float64 `json:"p95"`
	RPS       float64  `json:"rps"`
	ErrorRate *float64 `json:"errorRate"`
	CPU       float64  `json:"cpu"`
}

type runDetail struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Error       string          `json:"error"`
	Windows     []window        `json:"windows"`
	Summary     json.RawMessage `json:"summary"`
	Versions    json.RawMessage `json:"versions"`
	FaultEvents []struct {
		State     string   `json:"state"`
		AppliedAt *float64 `json:"appliedAt"`
		Error     string   `json:"error"`
	} `json:"faultEvents"`
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64{}, v...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

func run(a arm, scID, cond string, rep, order int) Result {
	r := Result{Arm: a.Name, Cond: cond, Rep: rep, Order: order}
	var started struct {
		ID string `json:"id"`
	}
	if err := call("POST", a.API+"/api/scenarios/"+scID+"/runs", nil, &started); err != nil {
		r.Status, r.Error = "não iniciou", err.Error()
		return r
	}
	r.RunID = started.ID
	var d runDetail
	deadline := time.Now().Add(time.Duration(*duration)*time.Second + 3*time.Minute)
	for {
		time.Sleep(2 * time.Second)
		d = runDetail{}
		if err := call("GET", a.API+"/api/runs/"+r.RunID, nil, &d); err != nil {
			r.Status, r.Error = "erro lendo", err.Error()
			return r
		}
		if d.Status != "pending" && d.Status != "running" {
			break
		}
		if time.Now().After(deadline) {
			r.Status, r.Error = "tempo esgotado", "a execução não terminou"
			return r
		}
	}
	r.Status, r.Error, r.Summary, r.Versions = d.Status, d.Error, d.Summary, d.Versions
	for _, f := range d.FaultEvents {
		if f.AppliedAt != nil {
			r.FaultFrom = *f.AppliedAt
		}
		if f.State != "reverted" {
			r.Status, r.Error = "falha não aplicada", f.State+" "+f.Error
		}
	}
	// Janela medida: do fim do aquecimento ao início da descida do perfil plateau (últimos 10%).
	from, to := *warmup, *duration-*duration/10
	var rps, p50, p95, cpu []float64
	var reqs, failed float64
	for _, w := range d.Windows {
		if w.T < from || w.T >= to {
			continue
		}
		rps = append(rps, w.RPS)
		cpu = append(cpu, w.CPU)
		reqs += w.RPS
		if w.ErrorRate != nil {
			failed += w.RPS * *w.ErrorRate
		}
		if w.P50 != nil {
			p50 = append(p50, *w.P50)
		}
		if w.P95 != nil {
			p95 = append(p95, *w.P95)
		}
	}
	if len(rps) > 0 {
		r.WinRPS = reqs / float64(len(rps))
		r.WinP50, r.WinP95 = median(p50), median(p95)
		var c float64
		for _, v := range cpu {
			c += v
		}
		r.WinCPU = c / float64(len(cpu))
		if reqs > 0 {
			r.WinErrPct = failed / reqs * 100
		}
	}
	return r
}

// bootstrap devolve o intervalo de 95% da diferença das medianas (b - a), reamostrando as execuções.
func bootstrap(a, b []float64, rng *rand.Rand) (lo, hi float64) {
	const n = 20000
	diffs := make([]float64, n)
	pick := func(v []float64) []float64 {
		s := make([]float64, len(v))
		for i := range s {
			s[i] = v[rng.Intn(len(v))]
		}
		return s
	}
	for i := range diffs {
		diffs[i] = median(pick(b)) - median(pick(a))
	}
	sort.Float64s(diffs)
	return diffs[int(0.025*n)], diffs[int(0.975*n)-1]
}

func minmax(v []float64) (float64, float64) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, x := range v {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return lo, hi
}

func main() {
	flag.Parse()
	arms := []arm{{"antes", *before, false}, {"controle", *after, false}, {"taxa", *after, true}}
	ids := map[string]string{}
	for _, a := range arms {
		for _, lat := range []bool{false, true} {
			var created struct {
				ID string `json:"id"`
			}
			if err := call("POST", a.API+"/api/scenarios", scenario(a, lat), &created); err != nil {
				fmt.Fprintln(os.Stderr, "criando cenário:", err)
				os.Exit(1)
			}
			ids[fmt.Sprint(a.Name, lat)] = created.ID
		}
	}

	var results []Result
	order := 0
	for rep := 1; rep <= *reps; rep++ {
		// Intercala braços e condições, girando a ordem a cada repetição: nenhuma condição
		// roda sempre primeiro nem sempre depois da mesma vizinha (deriva ao longo do tempo).
		for i := range arms {
			a := arms[(i+rep)%len(arms)]
			conds := []bool{false, true}
			if (rep+i)%2 == 1 {
				conds = []bool{true, false}
			}
			for _, lat := range conds {
				cond := "lat0"
				if lat {
					cond = fmt.Sprintf("lat%d", *latency)
				}
				order++
				r := run(a, ids[fmt.Sprint(a.Name, lat)], cond, rep, order)
				results = append(results, r)
				fmt.Printf("%2d rep %d %-8s %-6s %-9s rps=%8.1f p50=%7.2f p95=%7.2f err=%.2f%% cpu=%5.1f %s\n",
					order, rep, a.Name, cond, r.Status, r.WinRPS, r.WinP50, r.WinP95, r.WinErrPct, r.WinCPU, r.Error)
				b, _ := json.MarshalIndent(results, "", " ")
				_ = os.WriteFile(*out+"/execucoes.json", b, 0o644)
				time.Sleep(5 * time.Second) // o alvo esvazia filas e conexões antes da próxima
			}
		}
	}

	// --- tabelas ---
	rng := rand.New(rand.NewSource(1)) // semente fixa: o mesmo JSON dá o mesmo intervalo
	var md strings.Builder
	get := func(arm, cond string, f func(Result) float64) []float64 {
		var v []float64
		for _, r := range results {
			if r.Arm == arm && r.Cond == cond && r.Status == "completed" {
				v = append(v, f(r))
			}
		}
		return v
	}
	rpsOf := func(r Result) float64 { return r.WinRPS }
	latCond := fmt.Sprintf("lat%d", *latency)
	md.WriteString("| Braço | Condição | Execuções válidas | RPS mediana | RPS mín-máx | p50 mediana (ms) | p95 mediana (ms) | Erro (%) | CPU do alvo (%) |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range arms {
		for _, c := range []string{"lat0", latCond} {
			v := get(a.Name, c, rpsOf)
			lo, hi := minmax(v)
			fmt.Fprintf(&md, "| %s | %s | %d de %d | %.1f | %.1f a %.1f | %.2f | %.2f | %.2f | %.0f |\n", a.Name, c, len(v), *reps, median(v), lo, hi,
				median(get(a.Name, c, func(r Result) float64 { return r.WinP50 })), median(get(a.Name, c, func(r Result) float64 { return r.WinP95 })),
				median(get(a.Name, c, func(r Result) float64 { return r.WinErrPct })), median(get(a.Name, c, func(r Result) float64 { return r.WinCPU })))
		}
	}
	md.WriteString("\n| Braço | RPS sem latência | RPS com latência | Diferença das medianas | Intervalo de 95% (bootstrap) | Em % da carga sem latência |\n|---|---|---|---|---|---|\n")
	for _, a := range arms {
		v0, v1 := get(a.Name, "lat0", rpsOf), get(a.Name, latCond, rpsOf)
		if len(v0) == 0 || len(v1) == 0 {
			continue
		}
		lo, hi := bootstrap(v0, v1, rng)
		m0, m1 := median(v0), median(v1)
		fmt.Fprintf(&md, "| %s | %.1f | %.1f | %+.1f | %+.1f a %+.1f | %+.1f%% (de %+.1f%% a %+.1f%%) |\n", a.Name, m0, m1, m1-m0, lo, hi, (m1-m0)/m0*100, lo/m0*100, hi/m0*100)
	}
	// O controle contra o antes, sem latência: a medição nova mudou o que é medido?
	if v0, v1 := get("antes", "lat0", rpsOf), get("controle", "lat0", rpsOf); len(v0) > 0 && len(v1) > 0 {
		lo, hi := bootstrap(v0, v1, rng)
		fmt.Fprintf(&md, "\nControle contra antes (VUs, sem latência): diferença das medianas de RPS %+.1f (intervalo de 95%% de %+.1f a %+.1f; %+.1f%%).\n",
			median(v1)-median(v0), lo, hi, (median(v1)-median(v0))/median(v0)*100)
	}
	_ = os.WriteFile(*out+"/tabelas.md", []byte(md.String()), 0o644)
	fmt.Println("\n" + md.String())
}
