// Package stats decide se duas séries de execuções diferem: a regra que diz "melhorou",
// "piorou", "sem diferença" ou "inconclusivo" num experimento A/B. É irmão do pacote verdict:
// sem IA e reproduzível (os mesmos números dão sempre a mesma decisão).
//
// A unidade é a execução, não o segundo: os segundos de uma mesma execução são correlacionados
// entre si, e tratá-los como amostras independentes fabrica significância falsa.
package stats

import (
	"math"
	"math/rand"
	"sort"
)

// MinRuns é o mínimo de execuções válidas por versão para decidir. Com 3 por versão um teste de
// postos nem chega a p < 0,05 (o menor valor possível é 0,10); com 5 chega a 0,008.
const MinRuns = 5

// Resamples é o número de reamostragens do bootstrap.
const Resamples = 20000

// seed fixa: a decisão não pode mudar entre duas leituras do mesmo experimento.
const seed = 1

// Direction diz para que lado a métrica melhora.
type Direction string

const (
	LowerIsBetter  Direction = "lower"  // latência, taxa de erro, CPU por requisição
	HigherIsBetter Direction = "higher" // nota de resiliência
)

// Outcome é a decisão.
type Outcome string

const (
	Improved     Outcome = "melhorou"
	Worsened     Outcome = "piorou"
	NoDifference Outcome = "sem_diferenca"
	Inconclusive Outcome = "inconclusivo"
)

// Margin é o limite prático: uma diferença menor que ele não interessa, mesmo que seja real.
// Vale o maior entre Pct% da mediana de A e Abs (na unidade da métrica; cobre medianas perto
// de zero, como uma taxa de erro de 0%).
type Margin struct {
	Pct float64 `json:"pct"`
	Abs float64 `json:"abs"`
}

func (m Margin) of(medianA float64) float64 {
	return math.Max(math.Abs(medianA)*m.Pct/100, m.Abs)
}

// Comparison é o resultado de comparar B com A numa métrica.
type Comparison struct {
	NA      int     `json:"nA"`
	NB      int     `json:"nB"`
	MedianA float64 `json:"medianA"`
	MedianB float64 `json:"medianB"`
	// Diff é a diferença das medianas (B - A); Lo e Hi, o intervalo de 95% dela (bootstrap).
	Diff float64 `json:"diff"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
	// Em % da mediana de A (zero quando a mediana de A é zero).
	DiffPct float64 `json:"diffPct"`
	LoPct   float64 `json:"loPct"`
	HiPct   float64 `json:"hiPct"`
	// MarginAbs é o limite prático usado, na unidade da métrica.
	MarginAbs float64 `json:"marginAbs"`
	Outcome   Outcome `json:"outcome"`
	// Why explica a decisão em uma frase.
	Why string `json:"why"`
}

// Median devolve a mediana (NaN para uma série vazia).
func Median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	n := len(s)
	return (s[n/2-1] + s[n/2]) / 2
}

// MedianDiffInterval devolve o intervalo de 95% da diferença das medianas (B - A), por
// bootstrap percentil: reamostra as execuções de cada lado, com reposição.
func MedianDiffInterval(a, b []float64) (lo, hi float64) {
	rng := rand.New(rand.NewSource(seed))
	diffs := make([]float64, Resamples)
	sa, sb := make([]float64, len(a)), make([]float64, len(b))
	for i := range diffs {
		for j := range sa {
			sa[j] = a[rng.Intn(len(a))]
		}
		for j := range sb {
			sb[j] = b[rng.Intn(len(b))]
		}
		diffs[i] = Median(sb) - Median(sa)
	}
	sort.Float64s(diffs)
	return diffs[int(0.025*Resamples)], diffs[int(0.975*Resamples)-1]
}

// Compare decide se B difere de A.
//
//   - sem diferença: o intervalo inteiro cabe dentro do limite prático (qualquer diferença
//     real é pequena demais para importar);
//   - melhorou: o intervalo exclui zero para o lado bom e o ganho passa do limite prático;
//   - piorou: o intervalo exclui zero para o lado ruim;
//   - inconclusivo: todo o resto, inclusive menos de MinRuns execuções de um lado. É um
//     resultado legítimo: os dados não bastam para afirmar nada.
func Compare(a, b []float64, dir Direction, m Margin) Comparison {
	c := Comparison{NA: len(a), NB: len(b), Outcome: Inconclusive}
	if len(a) == 0 || len(b) == 0 {
		c.Why = "sem execuções válidas dos dois lados"
		return c
	}
	c.MedianA, c.MedianB = Median(a), Median(b)
	c.Diff = c.MedianB - c.MedianA
	c.Lo, c.Hi = MedianDiffInterval(a, b)
	if c.MedianA != 0 {
		k := 100 / math.Abs(c.MedianA)
		c.DiffPct, c.LoPct, c.HiPct = c.Diff*k, c.Lo*k, c.Hi*k
	}
	c.MarginAbs = m.of(c.MedianA)
	if len(a) < MinRuns || len(b) < MinRuns {
		c.Why = "menos de 5 execuções válidas por versão"
		return c
	}
	// Em "good", positivo é melhora.
	gLo, gHi, gain := c.Lo, c.Hi, c.Diff
	if dir == LowerIsBetter {
		gLo, gHi, gain = -c.Hi, -c.Lo, -c.Diff
	}
	switch {
	case gLo >= -c.MarginAbs && gHi <= c.MarginAbs:
		c.Outcome, c.Why = NoDifference, "o intervalo de 95% da diferença cabe inteiro dentro do limite prático"
	case gLo > 0 && gain >= c.MarginAbs:
		c.Outcome, c.Why = Improved, "o intervalo de 95% exclui zero para o lado bom e o ganho passa do limite prático"
	case gHi < 0:
		c.Outcome, c.Why = Worsened, "o intervalo de 95% exclui zero para o lado ruim"
	case gLo > 0:
		c.Why = "há ganho, mas menor que o limite prático"
	default:
		c.Why = "o intervalo de 95% inclui zero e é mais largo que o limite prático: o ruído não deixa afirmar"
	}
	return c
}
