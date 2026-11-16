package stats

import (
	"math"
	"testing"
)

var latency = Margin{Pct: 5}

func TestMedian(t *testing.T) {
	if Median([]float64{3, 1, 2}) != 2 || Median([]float64{4, 1, 2, 3}) != 2.5 || !math.IsNaN(Median(nil)) {
		t.Fatal("mediana errada")
	}
}

// A/A: a mesma versão dos dois lados, com o ruído de sempre, é "sem diferença".
func TestSameVersionIsNoDifference(t *testing.T) {
	a := []float64{100.2, 99.1, 101.0, 100.4, 99.8}
	b := []float64{99.9, 100.6, 100.1, 99.4, 100.9}
	c := Compare(a, b, LowerIsBetter, latency)
	if c.Outcome != NoDifference {
		t.Fatalf("A/A deveria dar sem diferença: %+v", c)
	}
	// Séries idênticas e sem variação (ex.: RPS com taxa de chegada): intervalo [0, 0].
	flat := []float64{250, 250, 250, 250, 250}
	if c := Compare(flat, flat, HigherIsBetter, latency); c.Outcome != NoDifference || c.Lo != 0 || c.Hi != 0 {
		t.Fatalf("séries idênticas: %+v", c)
	}
}

// Um atraso de 20 ms num p95 de ~5 ms é "piorou", sem ambiguidade.
func TestDelayIsWorsened(t *testing.T) {
	a := []float64{4.9, 5.1, 5.0, 4.8, 5.2}
	b := []float64{24.8, 25.3, 25.0, 24.9, 25.1}
	c := Compare(a, b, LowerIsBetter, latency)
	if c.Outcome != Worsened || c.Diff < 19 || c.Lo <= 0 || c.DiffPct < 300 {
		t.Fatalf("esperava piorou com ~+20 ms: %+v", c)
	}
	// A mesma diferença numa métrica em que maior é melhor (nota) é melhora.
	if c := Compare(a, b, HigherIsBetter, latency); c.Outcome != Improved {
		t.Fatalf("maior é melhor: %+v", c)
	}
}

func TestImprovedNeedsPracticalGain(t *testing.T) {
	a := []float64{100, 101, 99, 100.5, 99.5}
	faster := []float64{80, 81, 79, 80.5, 79.5}
	if c := Compare(a, faster, LowerIsBetter, latency); c.Outcome != Improved || c.DiffPct > -19 {
		t.Fatalf("20%% mais rápido deveria ser melhorou: %+v", c)
	}
	// 2% mais rápido, bem medido: o intervalo inteiro fica dentro dos 5%, então não importa.
	slightly := []float64{98, 98.1, 97.9, 98.05, 97.95}
	if c := Compare(a, slightly, LowerIsBetter, latency); c.Outcome != NoDifference {
		t.Fatalf("ganho de 2%% com limite de 5%%: %+v", c)
	}
}

// Ruído maior que o efeito: nem melhor, nem pior, nem igual. Os dados não bastam.
func TestNoisyIsInconclusive(t *testing.T) {
	a := []float64{80, 120, 95, 140, 100}
	b := []float64{85, 150, 90, 110, 70}
	if c := Compare(a, b, LowerIsBetter, latency); c.Outcome != Inconclusive {
		t.Fatalf("esperava inconclusivo: %+v", c)
	}
}

func TestNeedsMinRuns(t *testing.T) {
	a := []float64{5, 5, 5, 5}
	b := []float64{25, 25, 25, 25}
	if c := Compare(a, b, LowerIsBetter, latency); c.Outcome != Inconclusive || c.MedianB != 25 {
		t.Fatalf("com 4 execuções por lado não decide, mas mostra os números: %+v", c)
	}
	if c := Compare(nil, b, LowerIsBetter, latency); c.Outcome != Inconclusive {
		t.Fatalf("sem execuções de A: %+v", c)
	}
}

// Taxa de erro com mediana zero em A: o limite em % não serve; vale o absoluto.
func TestAbsoluteMarginNearZero(t *testing.T) {
	zero := []float64{0, 0, 0, 0, 0}
	errs := Margin{Pct: 5, Abs: 0.1}
	if c := Compare(zero, []float64{0, 0.02, 0, 0.01, 0}, LowerIsBetter, errs); c.Outcome != NoDifference {
		t.Fatalf("erro de 0,01%% contra 0%% não é piora: %+v", c)
	}
	if c := Compare(zero, []float64{2, 2.5, 1.8, 2.2, 2.1}, LowerIsBetter, errs); c.Outcome != Worsened {
		t.Fatalf("erro de 2%% contra 0%% é piora: %+v", c)
	}
}

// A mesma entrada dá sempre a mesma decisão e o mesmo intervalo.
func TestDeterministic(t *testing.T) {
	a := []float64{80, 120, 95, 140, 100}
	b := []float64{85, 150, 90, 110, 70}
	if Compare(a, b, LowerIsBetter, latency) != Compare(a, b, LowerIsBetter, latency) {
		t.Fatal("o bootstrap tem que ser reproduzível")
	}
}
