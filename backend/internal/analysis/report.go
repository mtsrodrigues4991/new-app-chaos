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
	// NewFile marca um arquivo citado que não existe no repositório (a criar).
	NewFile bool `json:"newFile,omitempty"`
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
		fix["file"] = str("Arquivo a mudar (ou a criar), relativo à raiz do repositório, exatamente como aparece na árvore e nas ferramentas; vazio se a correção não é no código")
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
Analisa execuções do app new-app-chaos: carga com k6 e/ou falhas injetadas no alvo (latência, perda de pacotes, estresse de CPU e memória, kill). O alvo é um container Docker (falhas com tc netem, um estressor e stop/start do container) ou um Deployment do Kubernetes (falhas com o Chaos Mesh; o kill apaga um ou todos os pods e o Deployment recria). O cenário diz qual é a plataforma. Só no Kubernetes há também falhas de IO (io_latency/io_fault, num volume do container), de DNS (dns_error: a resolução de nomes falha) e de HTTP (http_delay/http_abort nas requisições que chegam aos pods), e uma falha pode ser aplicada numa dependência do alvo (outro Deployment, ex.: o banco): na coluna "falha", tipo@namespace/deployment é a falha na dependência e tipo>namespace/deployment é a falha de rede só no caminho do alvo até ela. Nesses casos o alvo continua sendo quem recebe a carga, e o efeito medido é o que a falha da dependência causa nele. No modo "só caos" não há carga: uma sonda faz um GET por segundo num endpoint.

Como ler os dados:
- t é o segundo desde o início da execução. A coluna "falha" diz qual falha estava ativa naquele segundo.
- Latência e erro ficam vazios num segundo sem requisição concluída. "presa" > 0 significa usuários virtuais ativos e nenhuma resposta (alvo travado ou fora do ar); conte isso como indisponibilidade.
- cpu_pct é do alvo (100 = 1 núcleo inteiro); mem_mb é a memória usada. No Kubernetes são a soma dos pods do Deployment, vindas do metrics-server, que atualiza a cada ~15 s (valores repetidos entre leituras são normais). pods_prontos/pods_total mostram quantos pods estavam prontos (pior momento do bloco).
- O veredito e a nota (0 a 100) foram calculados pelo app a partir dos SLOs do cenário, sem IA: cada falha vale 70% pelo tempo dentro dos SLOs enquanto ativa e 30% pela recuperação (cheia até 5 s, zero a partir de 60 s); com falhas, a linha de base pesa 20% e as falhas 80%. Explique a nota, não a recalcule nem a contradiga.
- Quando vierem "Eventos do Kubernetes" e "Logs dos containers", o t de cada linha está na mesma escala da série (segundos desde o início). Cruze-os com a coluna "falha" e as métricas: diga o que o alvo e as dependências registraram durante cada falha (ex.: erro de conexão com o banco, pool esgotado, timeout, reinício, readiness falhando) e quanto tempo depois voltaram ao normal. Cite a linha ou o evento com o t. Linhas "[xN até t=...]" se repetiram N vezes. Quando vier "Telemetria da aplicação", use-a para explicar o porquê: heap e pausas de GC, threads, requisições recebidas (taxa, p95, 5xx), chamadas de saída, pool de conexões do banco (em uso, esperando conexão, tempo de espera), os traces com erro e lentos (os spans dizem onde o tempo foi gasto e qual exceção ocorreu) e o profiling (funções que mais consumiram CPU ou alocaram memória na execução inteira). Cite valores e o t. Diga quando uma métrica que ajudaria não existe. As séries de taxa e p95 do Prometheus chegam ~5 a 15 s atrasadas em relação à sonda, à série do k6 e à coluna "falha" (o histograma é registrado quando a requisição termina, e a taxa usa uma janela de 10 s): compare pelo formato e pela ordem dos eventos, não pelo segundo exato. O profiling já vem sem o custo do próprio agente de observabilidade. Os eventos do Kubernetes têm precisão de 1 s (o instante é truncado), então podem aparecer até ~1 s antes do que a série e os logs mostram; não trate essa diferença como atraso ou relógio errado. Se os logs não mostram nada durante uma falha, diga isso; não invente mensagens.

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

Arquivos do repositório:
<arvore>
%s
</arvore>

Como investigar:
- Use a árvore acima para escolher o que abrir; não invente caminhos nem extensões (ex.: se existe application.yaml, não cite application.yml).
- Antes de concluir, abra com read_file pelo menos: o código que atende os endpoints testados (método e caminho do cenário), a configuração principal da aplicação (ex.: application.yaml/properties, .env de exemplo, config do servidor) e o arquivo de dependências (pom.xml, build.gradle, package.json, go.mod, requirements.txt), além de Dockerfile e compose se existirem.
- Procure o que explica o comportamento observado: threads e filas do servidor, pool de conexões e timeouts de banco ou de chamadas externas, retry e circuit breaker, cache, logs no caminho quente, limites de CPU e memória, healthcheck e tempo de partida.
- No máximo %d arquivos podem ser abertos; reler outro trecho do mesmo arquivo não conta.

Regras para citar código:
- Só cite um arquivo existente depois de abri-lo com read_file, com a linha que você viu. Se a configuração não está no arquivo, diga isso e aponte a linha onde ela entraria.
- Escreva o trecho proposto no formato e na linguagem do próprio arquivo (YAML num .yaml, properties num .properties, Java num .java).
- Antes de supor uma biblioteca ou recurso (ex.: Spring Boot Actuator, Resilience4j), confira no arquivo de dependências; se não estiver, inclua a dependência na sugestão.
- Para um arquivo que ainda não existe (ex.: Dockerfile), use o caminho onde ele deve ser criado, line 0, e diga que é um arquivo novo.
- Link de um arquivo no GitHub: %s (troque CAMINHO e N).`,
		where, s.Branch, s.Commit, b.Tree(), b.MaxReads, s.URL("CAMINHO", 0)+"#LN")
}

const codeReportTask = `Nas sugestões de correção, preencha file e line com o ponto exato no código (ou na configuração) e code com o trecho proposto.`

const codeChatTask = `Para perguntas sobre o código, use as ferramentas. Ao citar um arquivo, use um link markdown no formato [caminho:linha](link do GitHub).`

const chatInstructions = `

Agora você conversa com o usuário sobre a execução analisada. Responda à pergunta com base nas evidências, no relatório (se houver) e no histórico do mesmo cenário. Seja breve: parágrafos curtos e listas com "-" quando ajudar. Não use tabelas nem títulos. Se a pergunta não puder ser respondida com os dados, diga o que faltaria medir.`

// checkFix confere o arquivo citado numa correção: corrige o caminho quando a IA
// errou só a extensão ou a pasta, descarta linha fora do arquivo e não gera link
// para arquivo que não existe (marca como arquivo novo).
func checkFix(b *coderepo.Browser, f *Fix) {
	if strings.TrimSpace(f.File) == "" {
		f.File, f.Line, f.URL = "", 0, ""
		return
	}
	found, lines, ok := b.Locate(f.File)
	if !ok {
		f.NewFile, f.Line, f.URL = true, 0, ""
		return
	}
	f.File = found
	if f.Line < 0 || f.Line > lines {
		f.Line = 0
	}
	f.URL = b.Snap.URL(f.File, f.Line)
}

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
	b.WriteString(Describe("Execução analisada", in.Current, MaxRows, DiagChars))
	if in.Reference != nil {
		b.WriteString("\n")
		// A referência vai mais resumida: o que importa é a comparação.
		b.WriteString(Describe("Execução de referência (mesmo cenário)", *in.Reference, 120, RefDiagChars))
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
		for i := range rep.Content.Fixes {
			checkFix(in.Code, &rep.Content.Fixes[i])
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
