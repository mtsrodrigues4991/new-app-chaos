// Prova da Fase 1 (docs/analise-evolucao.md): um teste A/A tem que dar "sem diferença" e uma
// versão B com atraso artificial tem que dar "piorou". Cria o cenário e os dois experimentos
// pela API, como a tela faria, e espera a decisão. Quem sobe o ambiente é o scripts/bench-fase1.sh.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

var (
	api      = flag.String("api", "http://nacbench-after:8080", "API do backend")
	targetA  = flag.String("a", "nacbench-demo-api", "container da versão A")
	targetB  = flag.String("b", "nacbench-demo-api-b", "container da versão B (com atraso)")
	duration = flag.Int("duration", 40, "duração de cada execução (s)")
	rate     = flag.Int("rate", 200, "taxa de chegada (req/s)")
	out      = flag.String("out", "/out", "pasta dos resultados")
)

type obj = map[string]any

func call(method, url string, body, into any) error {
	rd := bytes.NewReader(nil)
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

type comparison struct {
	NA, NB                             int
	MedianA, MedianB, Diff, Lo, Hi     float64
	DiffPct, LoPct, HiPct              float64
	Outcome, Why                       string
}

type experiment struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error"`
	Runs   []struct {
		Arm     string             `json:"arm"`
		Valid   bool               `json:"valid"`
		Invalid string             `json:"invalid"`
		Values  map[string]float64 `json:"values"`
	} `json:"runs"`
	Result *struct {
		Outcome  string     `json:"outcome"`
		Primary  comparison `json:"primary"`
		Guards   []struct {
			Metric string `json:"metric"`
			comparison
		} `json:"guards"`
		NoisePct *float64 `json:"noisePct"`
		Notes    []string `json:"notes"`
	} `json:"result"`
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "erro:", err)
	os.Exit(1)
}

func runExperiment(name string, spec obj) (experiment, json.RawMessage) {
	var e experiment
	if err := call("POST", *api+"/api/experiments", spec, &e); err != nil {
		fail(err)
	}
	fmt.Printf("\n== %s (experimento %s)\n", name, e.ID)
	seen := 0
	for {
		time.Sleep(3 * time.Second)
		var raw json.RawMessage
		if err := call("GET", *api+"/api/experiments/"+e.ID, nil, &raw); err != nil {
			fail(err)
		}
		e = experiment{}
		if err := json.Unmarshal(raw, &e); err != nil {
			fail(err)
		}
		for ; seen < len(e.Runs); seen++ {
			r := e.Runs[seen]
			fmt.Printf("%2d %s p95=%7.2f ms erro=%.2f%% válida=%v %s\n", seen+1, r.Arm, r.Values["p95"], r.Values["error_rate"], r.Valid, r.Invalid)
		}
		if e.Status != "pending" && e.Status != "running" {
			return e, raw
		}
	}
}

func main() {
	flag.Parse()
	sc := obj{
		"name":   "fase1-demo",
		"target": obj{"platform": "docker", "container": *targetA, "baseUrl": "http://" + *targetA + ":8080"},
		"mode":   "load",
		"endpoints": []obj{
			{"method": "GET", "path": "/items", "weight": 9},
			{"method": "GET", "path": "/compute", "weight": 1},
		},
		"profile": obj{"type": "plateau", "vus": 100, "rate": *rate, "durationSeconds": *duration, "warmupSeconds": 10},
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := call("POST", *api+"/api/scenarios", sc, &created); err != nil {
		fail(err)
	}
	metric := obj{"name": "p95"}
	aa, rawAA := runExperiment("A/A: a mesma versão dos dois lados", obj{"scenarioId": created.ID, "metric": metric})
	ab, rawAB := runExperiment("A/B: B com atraso artificial", obj{"scenarioId": created.ID, "metric": metric,
		"b": obj{"platform": "docker", "container": *targetB, "baseUrl": "http://" + *targetB + ":8080"}})
	all, _ := json.MarshalIndent(obj{"aa": rawAA, "ab": rawAB}, "", " ")
	_ = os.WriteFile(*out+"/experimentos.json", all, 0o644)

	ok := true
	for _, c := range []struct {
		name, want string
		e          experiment
	}{{"A/A", "sem_diferenca", aa}, {"A/B com atraso", "piorou", ab}} {
		if c.e.Result == nil {
			fmt.Printf("\n%s: %s sem resultado (%s)\n", c.name, c.e.Status, c.e.Error)
			ok = false
			continue
		}
		p := c.e.Result.Primary
		fmt.Printf("\n%s: %s (esperado: %s)\n  p95 mediano A=%.2f ms, B=%.2f ms, diferença %+.2f ms (%+.1f%%), intervalo de 95%% de %+.2f a %+.2f ms; %d e %d execuções válidas\n",
			c.name, c.e.Result.Outcome, c.want, p.MedianA, p.MedianB, p.Diff, p.DiffPct, p.Lo, p.Hi, p.NA, p.NB)
		for _, g := range c.e.Result.Guards {
			fmt.Printf("  guarda %s: A=%.4f B=%.4f (%+.1f%%) -> %s\n", g.Metric, g.MedianA, g.MedianB, g.DiffPct, g.Outcome)
		}
		if c.e.Result.NoisePct != nil {
			fmt.Printf("  piso de ruído: %.1f%%\n", *c.e.Result.NoisePct)
		}
		for _, n := range c.e.Result.Notes {
			fmt.Println("  -", n)
		}
		if c.e.Status != "completed" || c.e.Result.Outcome != c.want {
			ok = false
		}
	}
	if !ok {
		fmt.Println("\nFALHOU: o critério de pronto da Fase 1 não foi atendido")
		os.Exit(1)
	}
	fmt.Println("\nOK: critério de pronto da Fase 1 atendido")
}
