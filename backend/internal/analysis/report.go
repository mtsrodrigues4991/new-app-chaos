package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/ai"
	"github.com/mtsrodrigues4991/new-app-chaos/backend/internal/coderepo"
)

// Content é o relatório que a IA devolve (formato garantido pelo JSON Schema abaixo).
type Content struct {
	Summary          string          `json:"summary"`
	ScoreExplanation string          `json:"scoreExplanation"`
	BreakingPoint    BreakingPoint   `json:"breakingPoint"`
	FaultFindings    []FaultFinding  `json:"faultFindings"`
	Causes           []string        `json:"causes"`
	Fixes            []Fix           `json:"fixes"`
	Regression       *RegressionPart `json:"regression,omitempty"`
}

type BreakingPoint struct {
	Found       bool   `json:"found"`
	Description string `json:"description"`
}

type FaultFinding struct {
	FaultID string `json:"faultId"`
	Finding string `json:"finding"`
}

type Fix struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// Com o código disponível: onde mudar e o trecho proposto ("" / 0 quando não se aplica).
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Code string `json:"code,omitempty"`
	URL  string `json:"url,omitempty"` // link para o arquivo no GitHub, no commit analisado
}

// CodeInfo diz qual versão do código a IA leu.
type CodeInfo struct {
	coderepo.Snapshot
	FilesRead []string `json:"filesRead"`
}

type RegressionPart struct {
	Verdict string   `json:"verdict"` // melhorou, igual, piorou
	Summary string   `json:"summary"`
	Items   []string `json:"items"`
}

// Report é o relatório salvo junto da execução.
type Report struct {
	Content        Content     `json:"content"`
	Provider       string      `json:"provider"`
	Usage          ai.Response `json:"usage"` // modelo que respondeu e tokens
	ReferenceRunID string      `json:"referenceRunId,omitempty"`
	Code           *CodeInfo   `json:"code,omitempty"`
	Score          int         `json:"score"` // nota no momento da análise
	CreatedAt      time.Time   `json:"createdAt"`
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func obj(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func arr(items map[string]any, desc string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": desc}
}

// Schema do relatório. A seção de regressão só existe quando há execução de referência.
func reportSchema(withRegression, withCode bool) map[string]any {
	fix := map[string]any{
		"title":  str("Correção em poucas palavras"),
		"detail": str("O que mudar e por quê, de forma concreta (configuração, código, infraestrutura)"),
	}
	fixRequired := []string{"title", "detail"}
	if withCode {
		fix["file"] = str("Arquivo a mudar, relativo à raiz do repositório (como nas ferramentas); vazio se a correção não é no código")
		fix["line"] = map[string]any{"type": "integer", "description": "Linha do ponto a mudar; 0 se não se aplica"}
		fix["code"] = str("Trecho de código ou configuração proposto (só o trecho alterado); vazio se não se aplica")
		fixRequired = append(fixRequired, "file", "line", "code")
	}
	props := map[string]any{
		"summary":          str("Resumo em 3 a 6 frases: o que foi testado, como a aplicação se comportou e o veredito."),
		"scoreExplanation": str("Por que a nota calculada ficou nesse valor, citando as falhas e os tempos de recuperação que mais pesaram."),
		"breakingPoint": obj(map[string]any{
			"found":       map[string]any{"type": "boolean", "description": "true se houve um ponto a partir do qual a aplicação deixou de cumprir os SLOs"},
			"description": str("Quando e em que condição quebrou (segundo, VUs, falha ativa, métrica que estourou). Se não quebrou, até onde foi testado sem quebrar."),
		}, "found", "description"),
		"faultFindings": arr(obj(map[string]any{
			"faultId": str("id da falha, igual ao do veredito"),
			"finding": str("Efeito observado durante e depois da falha, com números"),
		}, "faultId", "finding"), "Uma entrada por falha aplicada; vazio se não houve falhas."),
		"causes": arr(str(""), "Causas prováveis, da mais para a menos provável, cada uma apoiada em evidência da série."),
		"fixes":  arr(obj(fix, fixRequired...), "Sugestões de correção, da mais para a menos importante."),
	}
	required := []string{"summary", "scoreExplanation", "breakingPoint", "faultFindings", "causes", "fixes"}
	if withRegression {
		props["regression"] = obj(map[string]any{
			"verdict": map[string]any{"type": "string", "enum": []string{"melhorou", "igual", "piorou"}},
			"summary": str("Comparação com a execução de referência em 2 a 4 frases"),
			"items":   arr(str(""), "Diferenças relevantes, cada uma com os números das duas execuções"),
		}, "verdict", "summary", "items")
		required = append(required, "regression")
	}
	return obj(props, required...)
}

const baseInstructions = `Você é um engenheiro de confiabilidade (SRE) especialista em testes de carga e engenharia de caos.
Analisa execuções do app new-app-chaos: carga com k6 e/ou falhas injetadas num container Docker (latência e perda de pacotes com tc netem, estresse de CPU e memória, kill/restart do container). No modo "só caos" não há carga: uma sonda faz um GET por segundo num endpoint.

Como ler os dados:
- t é o segundo desde o início da execução. A coluna "falha" diz qual falha estava ativa naquele segundo.
- Latência e erro ficam vazios num segundo sem requisição concluída. "presa" > 0 significa usuários virtuais ativos e nenhuma resposta (alvo travado ou fora do ar); conte isso como indisponibilidade.
- cpu_pct é do container alvo (100 = 1 núcleo inteiro); mem_mb é a memória usada por ele.
- O veredito e a nota (0 a 100) foram calculados pelo app a partir dos SLOs do cenário, sem IA: cada falha vale 70% pelo tempo dentro dos SLOs enquanto ativa e 30% pela recuperação (cheia até 5 s, zero a partir de 60 s); com falhas, a linha de base pesa 20% e as falhas 80%. Explique a nota, não a recalcule nem a contradiga.

Regras:
- Responda em português do Brasil, direto, sem enrolação.
- Use só o que está nas evidências. Cite números e segundos. Quando algo for hipótese, diga que é hipótese.
- Nunca compare com execuções de outro cenário ou de outro alvo; só com a execução de referência ou o histórico do mesmo cenário que vierem nas evidências.`

const reportTask = `Escreva o relatório da execução analisada no formato pedido.`

const regressionTask = `Há uma execução de referência do mesmo cenário (marcada pelo usuário). Preencha também a seção "regression" comparando a execução analisada com ela. Se o cenário mudou entre as duas (carga, falhas ou SLOs), diga o que mudou e trate a comparação com cautela.`

// codeInstructions explica à IA como usar o código da aplicação.
func codeInstructions(b *coderepo.Browser) string {
	s := b.Snap
	where := s.Repo
	if s.Path != "" {
		where += " (subpasta " + s.Path + ")"
	}
	return fmt.Sprintf(`

Código da aplicação testada: você tem acesso só de leitura ao repositório %s, branch %s, commit %s, pelas ferramentas list_dir, search_code e read_file. Os caminhos são relativos à raiz da aplicação.
- Investigue no código o que explica o comportamento observado antes de concluir: o tratamento dos endpoints testados (método e caminho do cenário), configuração do servidor (threads, filas), pool de conexões e timeouts de banco ou de chamadas externas, retry e circuit breaker, cache, logs no caminho quente, Dockerfile e limites de CPU e memória.
- Comece por list_dir na raiz e por search_code; abra só o que importa (no máximo %d arquivos).
- Só cite arquivos e linhas que você viu nas ferramentas. Se o código não explicar algo, diga.
- Link de um arquivo no GitHub: %s (troque CAMINHO e N).`,
		where, s.Branch, s.Commit, b.MaxReads, s.URL("CAMINHO", 0)+"#LN")
}

const codeReportTask = `Nas sugestões de correção, preencha file e line com o ponto exato no código (ou na configuração) e code com o trecho proposto.`

const codeChatTask = `Para perguntas sobre o código, use as ferramentas. Ao citar um arquivo, use um link markdown no formato [caminho:linha](link do GitHub).`

const chatInstructions = `

Agora você conversa com o usuário sobre a execução analisada. Responda à pergunta com base nas evidências, no relatório (se houver) e no histórico do mesmo cenário. Seja breve: parágrafos curtos e listas com "-" quando ajudar. Não use tabelas nem títulos. Se a pergunta não puder ser respondida com os dados, diga o que faltaria medir.`

// Input reúne o que a análise precisa.
type Input struct {
	Current   RunData
	Reference *RunData          // execução de referência do mesmo cenário (nil se não houver ou se for a própria)
	History   []HistoryRow      // outras execuções do mesmo cenário (para o chat)
	Report    *Report           // relatório já salvo (para o chat)
	Code      *coderepo.Browser // código da aplicação (nil se o cenário não tem repositório)
}

func codeTools(b *coderepo.Browser) []ai.Tool {
	if b == nil {
		return nil
	}
	var out []ai.Tool
	for _, spec := range b.Specs() {
		name := spec.Name
		out = append(out, ai.Tool{Name: name, Description: spec.Description, Properties: spec.Properties, Required: spec.Required,
			Run: func(_ context.Context, in json.RawMessage) (string, error) { return b.Call(name, in) }})
	}
	return out
}

func evidence(in Input) string {
	var b strings.Builder
	b.WriteString("# Evidências\n\n")
	b.WriteString(Describe("Execução analisada", in.Current, MaxRows))
	if in.Reference != nil {
		b.WriteString("\n")
		// A referência vai mais resumida: o que importa é a comparação.
		b.WriteString(Describe("Execução de referência (mesmo cenário)", *in.Reference, 120))
	}
	if len(in.History) > 0 {
		fmt.Fprintf(&b, "\n## Histórico do mesmo cenário (mais recentes primeiro)\n```json\n%s\n```\n", jsonText(in.History))
	}
	if in.Report != nil {
		fmt.Fprintf(&b, "\n## Relatório já gerado para a execução analisada\n```json\n%s\n```\n", jsonText(in.Report.Content))
	}
	return b.String()
}

// Analyze gera o relatório sob demanda.
func Analyze(ctx context.Context, p ai.Provider, model string, in Input) (Report, error) {
	task := reportTask
	if in.Reference != nil {
		task += "\n" + regressionTask
	}
	system := baseInstructions
	if in.Code != nil {
		system += codeInstructions(in.Code)
		task += "\n" + codeReportTask
	}
	resp, err := p.Generate(ctx, ai.Request{
		Model:     model,
		System:    system,
		Context:   evidence(in),
		Messages:  []ai.Message{{Role: "user", Content: task}},
		Schema:    reportSchema(in.Reference != nil, in.Code != nil),
		MaxTokens: 16000,
		Tools:     codeTools(in.Code),
	})
	if err != nil {
		return Report{}, err
	}
	var c Content
	if err := json.Unmarshal([]byte(resp.Text), &c); err != nil {
		return Report{}, fmt.Errorf("a IA devolveu um relatório fora do formato: %w", err)
	}
	if in.Reference == nil {
		c.Regression = nil
	}
	rep := Report{Content: c, Provider: p.Info().ID, Usage: resp, Score: in.Current.Verdict.Score, CreatedAt: time.Now().UTC()}
	if in.Reference != nil {
		rep.ReferenceRunID = in.Reference.Run.ID
	}
	if in.Code != nil {
		rep.Code = &CodeInfo{Snapshot: in.Code.Snap, FilesRead: append([]string{}, in.Code.Read...)}
		for i, f := range rep.Content.Fixes {
			if f.File != "" {
				rep.Content.Fixes[i].URL = in.Code.Snap.URL(f.File, f.Line)
			}
		}
	}
	return rep, nil
}

// Chat responde a última pergunta de history (que deve terminar com uma mensagem do usuário).
func Chat(ctx context.Context, p ai.Provider, model string, in Input, history []ai.Message) (string, ai.Response, error) {
	system := baseInstructions
	if in.Code != nil {
		system += codeInstructions(in.Code)
	}
	system += chatInstructions
	if in.Code != nil {
		system += "\n" + codeChatTask
	}
	resp, err := p.Generate(ctx, ai.Request{
		Model:     model,
		System:    system,
		Context:   evidence(in),
		Messages:  history,
		MaxTokens: 8000,
		Tools:     codeTools(in.Code),
	})
	if err != nil {
		return "", resp, err
	}
	return strings.TrimSpace(resp.Text), resp, nil
}
