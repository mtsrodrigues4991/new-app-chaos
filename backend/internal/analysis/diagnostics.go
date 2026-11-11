package analysis

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/store"
)

// DiagChars é o orçamento de caracteres de logs e eventos da execução analisada; a de
// referência recebe RefDiagChars.
const (
	DiagChars    = 40000
	RefDiagChars = 6000
	maxLineChars = 400
)

// important casa linhas que costumam explicar uma falha.
var important = regexp.MustCompile(`(?i)(error|erro|exception|fatal|panic|warn|fail|refused|reset|timeout|timed out|denied|killed|oom|caused by|sqlstate|unavailable|terminat|shutdown|shutting down|starting|started|ready|listening|connection|pool|retry|circuit)`)

var (
	reHex    = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reDigits = regexp.MustCompile(`\d+`)
	reFrame  = regexp.MustCompile(`^\s+(at |\.\.\. \d+ more)`)
)

// normalize tira números e ids para juntar linhas que só mudam neles.
func normalize(s string) string {
	s = reHex.ReplaceAllString(s, "#")
	return reDigits.ReplaceAllString(s, "#")
}

type logEntry struct {
	first, last float64
	count       int
	text        string
	keep        bool
}

// condense junta linhas repetidas e escolhe as que importam: as que casam important (com
// até 3 linhas de stack trace depois), as 5 primeiras e as 15 últimas.
func condense(lines []store.LogLine) []*logEntry {
	byKey := map[string]*logEntry{}
	var order []*logEntry
	frames := 0
	for i, l := range lines {
		text := l.Text
		if len(text) > maxLineChars {
			text = text[:maxLineChars] + "…"
		}
		key := normalize(text)
		e := byKey[key]
		if e == nil {
			e = &logEntry{first: l.T, text: text}
			byKey[key] = e
			order = append(order, e)
		}
		e.last = l.T
		e.count++
		switch {
		case reFrame.MatchString(text):
			if frames > 0 {
				e.keep = true
				frames--
			}
		case important.MatchString(text):
			e.keep = true
			frames = 3
		default:
			frames = 0
		}
		if i < 5 || i >= len(lines)-15 {
			e.keep = true
		}
	}
	out := order[:0:0]
	for _, e := range order {
		if e.keep {
			out = append(out, e)
		}
	}
	return out
}

func fmtT(t float64) string {
	if t < 0 {
		return "?"
	}
	return trimFloat(t)
}

func (e *logEntry) String() string {
	s := fmt.Sprintf("t=%s %s", fmtT(e.first), e.text)
	if e.count > 1 {
		s += fmt.Sprintf("  [x%d até t=%s]", e.count, fmtT(e.last))
	}
	return s
}

// fit corta as linhas para caber em budget caracteres, mantendo o começo e o fim.
func fit(lines []string, budget int) []string {
	total := 0
	for _, l := range lines {
		total += len(l) + 1
	}
	if total <= budget {
		return lines
	}
	var head, tail []string
	used := 0
	for i, j := 0, len(lines)-1; i <= j; {
		if l := lines[i]; used+len(l)+1 <= budget/2 {
			head = append(head, l)
			used += len(l) + 1
			i++
			continue
		}
		if l := lines[j]; used+len(l)+1 <= budget {
			tail = append([]string{lines[j]}, tail...)
			used += len(l) + 1
			j--
			continue
		}
		head = append(head, fmt.Sprintf("… (%d linhas omitidas) …", j-i+1))
		break
	}
	return append(head, tail...)
}

// DescribeDiagnostics escreve os eventos do Kubernetes e os logs condensados.
func DescribeDiagnostics(d *store.Diagnostics, budget int) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	evBudget := budget / 4
	if len(d.Events) > 0 {
		b.WriteString("### Eventos do Kubernetes (t = segundos desde o início; negativo = pouco antes)\n```\n")
		var ls []string
		for _, e := range d.Events {
			l := fmt.Sprintf("t=%s %s %s %s/%s: %s", fmtT(e.T), e.Type, e.Reason, e.Namespace, e.Object, e.Message)
			if e.Count > 1 {
				l += fmt.Sprintf(" [x%d]", e.Count)
			}
			ls = append(ls, l)
		}
		b.WriteString(strings.Join(fit(ls, evBudget), "\n") + "\n```\n\n")
	}
	if len(d.Logs) > 0 {
		b.WriteString("### Logs dos containers durante a execução (condensados)\n")
		b.WriteString("Linhas iguais a menos de números e ids foram juntadas ([xN até t=...]); ficaram as que indicam erro, conexão, partida ou parada, e o começo e o fim de cada log.\n\n")
		per := (budget - evBudget) / len(d.Logs)
		for _, l := range d.Logs {
			title := l.Role + " " + l.Pod
			if l.Namespace != "" {
				title = l.Role + " " + l.Namespace + "/" + l.Pod
			}
			if l.Container != "" {
				title += " (container " + l.Container + ")"
			}
			var flags []string
			if l.Previous {
				flags = append(flags, "instância anterior, antes de reiniciar")
			}
			if l.Gone {
				flags = append(flags, "pod apagado durante a execução: o log vai até a última coleta")
			}
			if l.Truncated {
				flags = append(flags, "só as últimas linhas")
			}
			if len(flags) > 0 {
				title += "; " + strings.Join(flags, "; ")
			}
			entries := condense(l.Lines)
			fmt.Fprintf(&b, "#### %s: %d linhas, %d mostradas\n```\n", title, len(l.Lines), len(entries))
			ls := make([]string, len(entries))
			for i, e := range entries {
				ls[i] = e.String()
			}
			if len(ls) == 0 {
				ls = []string{"(sem linhas no período)"}
			}
			b.WriteString(strings.Join(fit(ls, per), "\n") + "\n```\n\n")
		}
	}
	if len(d.Notes) > 0 {
		b.WriteString("### O que não deu para coletar\n")
		for _, n := range d.Notes {
			b.WriteString("- " + n + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
