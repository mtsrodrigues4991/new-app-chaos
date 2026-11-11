# new-app-chaos

> Nome provisório.

App web que une **teste de carga (k6)**, **chaos engineering** e uma **IA que analisa cada execução**: o que aconteceu, onde a API quebra, o que regrediu e o que corrigir.

A especificação completa (visão, escopo do MVP, IA, interface, arquitetura, modelo de dados e roadmap) está em [docs/especificacao.md](docs/especificacao.md).

## Status

Marcos 1 (esqueleto), 2 (carga), 3 (caos) e 4 (IA) prontos:

- **Alvos:** lista os containers Docker rodando (nome, imagem, portas, rede) e cria um cenário a partir de um deles.
- **Cenários:** editor por formulário com alvo, URL base, endpoints (método, caminho, corpo, peso) e perfil de carga pronto (rampa, patamar ou pico) com prévia dos VUs. Salvos em SQLite.
- **Execução:** o backend sobe um container `grafana/k6` na mesma rede do alvo, lê as métricas já agregadas por segundo pelo dashboard embutido do k6 e transmite ao vivo (Server-Sent Events). Dá para abortar. Uma execução por vez.
- **Gráficos ao vivo e histórico:** latência p50/p95/p99, RPS, VUs ativos, respostas por código HTTP, taxa de erro, CPU e memória do container alvo. Tudo agregado por segundo e salvo, então execuções antigas podem ser reabertas.

- **Caos:** três modos por cenário (só carga, só caos, carga + caos). No editor, as falhas são blocos numa linha do tempo: arraste para mover, puxe as bordas para mudar a duração (setas do teclado também funcionam). Uma falha por vez neste marco. Falhas e parâmetros (nomes iguais aos do krkn):
  - **Latência** (`latency`, `jitter` em ms) e **perda de pacotes** (`loss` em %): `tc netem` aplicado por um container auxiliar que entra na rede do alvo. Vale para o tráfego que sai do alvo, ou seja, as respostas da API.
  - **CPU** (`cpu-load-percentage`, `cpu-workers`) e **memória** (`memory-vm-bytes`, ex. `256m`, `1g`, `80%`): um binário estático (`backend/cmd/stressor`) é copiado para dentro do alvo e executado lá, então a carga conta no limite do próprio container. Nada precisa ser instalado na imagem do alvo.
  - **Kill/restart:** derruba o container no início do bloco e o sobe de novo no fim.
  - Toda falha é revertida no fim do bloco, ao abortar e, se o backend reiniciar no meio, na subida seguinte. Se uma falha não puder ser aplicada ou revertida, a execução para e é marcada como falha.
  - No modo só caos, uma **sonda** faz um GET por segundo no endpoint escolhido (ex. `/health`) e mostra latência, disponibilidade e erros, junto com CPU e memória do alvo.
  - As faixas das falhas aparecem atrás de todos os gráficos, e os instantes reais de aplicação e reversão ficam salvos com a execução.

- **Veredito (sem IA, reproduzível):** cada cenário tem SLOs editáveis, com sugestão de p95 ≤ 500 ms e erro ≤ 1% nos modos com carga e disponibilidade da sonda ≥ 99% no modo só caos. Cada segundo da execução fica dentro ou fora deles, e a nota de resiliência (0 a 100) segue a ideia do Resiliency Score do krkn:
  - cada falha vale 70% pelo tempo dentro dos SLOs enquanto ativa e 30% pela recuperação (cheia até 5 s, zero a partir de 60 s; recuperado = 3 s seguidos dentro dos SLOs);
  - com falhas, a nota é 20% linha de base (tempo fora das falhas) + 80% média das falhas; sem falhas, é a porcentagem dos segundos dentro dos SLOs;
  - 90 ou mais = resiliente, 70 a 89 = atenção, abaixo de 70 = frágil. Pesos em `backend/internal/verdict`.
  - Segundos sem nenhuma resposta (alvo fora do ar) aparecem como lacuna nos gráficos e contam como fora dos SLOs. O timeout por requisição é configurável (padrão 10 s).
- **Análise com IA, sob demanda:** o botão "Analisar com IA" manda as evidências da execução (cenário, veredito, falhas com instantes reais, série por segundo com carga, sonda, CPU e memória) e salva um relatório com resumo, ponto de quebra, explicação da nota, efeito de cada falha, causas prováveis e correções.
  - **Regressão:** marque uma execução como referência do cenário (☆ no veredito). As próximas análises do mesmo cenário trazem a comparação com ela. Nunca compara com outro cenário ou alvo.
  - **Chat** em cada execução: a IA vê a execução, o relatório, a referência e o histórico do mesmo cenário. A conversa fica salva.
  - **Relatório imprimível:** o botão "Imprimir / salvar PDF" na execução gera um relatório em A4, sempre em tema claro, com números, veredito, falhas, gráficos e a análise da IA (sem o chat).
  - **Configurações:** escolhe provedor e modelo (padrão Claude Opus 5.5; Sonnet 5.5 e Haiku 5.5 são mais baratos). A camada de provedor (`backend/internal/ai`) é trocável.

Backlog: curva de carga desenhável, falhas combinadas, banda/DNS/disco, falhas na entrada (ingress).

## Rodando

Requisitos: Docker com Compose.

```bash
docker compose up --build
```

Para a análise por IA, crie um arquivo `.env` ao lado do `docker-compose.yml` (já está no `.gitignore`) com a sua chave da Anthropic e suba de novo:

```bash
cp .env.example .env   # e preencha ANTHROPIC_API_KEY
docker compose up -d
```

A chave fica só no backend: não vai para o banco nem para o navegador. Sem ela, todo o resto funciona e o botão da IA explica o que falta. Cada análise ou pergunta no chat consome créditos da sua conta (uma execução de 1 minuto manda uns 6 mil tokens; as longas são agrupadas em blocos e ficam por volta de 20 mil).

Na primeira execução de um cenário, o backend baixa a imagem `grafana/k6` (pode levar um minuto). Os dados ficam no volume `app-data`; para zerar, `docker compose down -v`.

As falhas de rede usam o módulo `netem` do kernel. A maioria das distribuições já o carrega sob demanda; se aparecer o erro "o kernel desta máquina não tem o módulo netem", rode `sudo modprobe sch_netem`.

- Frontend: http://localhost:3000 (comece em Alvos → Criar cenário no `demo-api`)
- Backend: http://localhost:8080/api/health
- API de exemplo: http://localhost:8081/items (collection do Postman com `/health`, `/items` e `/compute` em [examples/demo-api/postman_collection.json](examples/demo-api/postman_collection.json))

### Desenvolvimento sem Docker

```bash
# backend (Go 1.25+). Para a IA: export ANTHROPIC_API_KEY=... Para as falhas: compile o stressor e aponte uma imagem com tc
# (a do próprio backend serve: docker compose build backend).
cd backend && go build -o /tmp/stressor ./cmd/stressor
STRESSOR_PATH=/tmp/stressor TOOLS_IMAGE=new-app-chaos-backend go run ./cmd/server

# frontend (Node 22+)
cd frontend && npm install && npm run dev
```

## Estrutura

```
backend/            API em Go
  cmd/server/       ponto de entrada
  cmd/stressor/     gerador de carga de CPU/memória copiado para dentro do alvo
  internal/api/     rotas REST e stream ao vivo (SSE)
  internal/dockerx/ cliente da Docker Engine API via socket
  internal/scenario/ modelo: alvo, endpoints, perfil de carga, falhas, SLOs
  internal/load/    geração do script k6 e agregação das métricas
  internal/runner/  orquestra cada execução (k6, sonda, falhas, stats do alvo, streaming)
  internal/chaos/   injeção e reversão de falhas (tc netem, stressor, kill/restart)
  internal/probe/   sonda HTTP do modo só caos
  internal/verdict/ SLOs por segundo e nota de resiliência (sem IA)
  internal/analysis/ evidências para a IA, relatório e chat
  internal/ai/      camada de provedor de IA trocável (Anthropic/Claude no MVP)
  internal/store/   persistência em SQLite
frontend/           React + TypeScript + Vite
examples/demo-api/  API de exemplo para testes
docs/               especificação
```

## Roadmap do MVP

1. Esqueleto (feito)
2. Carga: editor de endpoints e perfil, execução do k6, gráficos ao vivo (feito; falta a curva desenhável)
3. Caos: injetor, as cinco falhas, linha do tempo arrastável, reversão garantida (feito)
4. IA: SLOs e veredito, relatório via Claude sob demanda, comparação com referência, chat (feito)

Depois: VMs via SSH, Kubernetes, outros provedores de IA, CLI para CI, SaaS.
