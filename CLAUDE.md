# CLAUDE.md

Guia para o Claude Code trabalhar neste repositório. Responda sempre em **português**.

## Regra número 1: entreviste antes de começar

Antes de **qualquer** tarefa, entreviste o Mateus:

1. Faça **uma pergunta por vez** até ter clareza sobre objetivo, contexto e resultado esperado.
2. Resuma o entendimento.
3. Só comece o trabalho **depois que ele confirmar** o resumo.

Não puxe itens do backlog nem "melhorias extras" sem ele pedir.

## O que é o projeto

**new-app-chaos** (nome provisório): teste de carga com k6 + injeção de falhas (caos) numa linha do tempo
+ nota de resiliência + análise e chat por IA (Claude) que apontam arquivo e linha no código do alvo.
Projeto pessoal/portfólio, com caminho aberto para open source ou SaaS.

Leia antes de mexer:
- [README.md](README.md): como rodar (Docker, KIND, Azure), arquitetura, estrutura de pastas e backlog.
- [docs/funcionalidades.md](docs/funcionalidades.md): o que cada funcionalidade faz em detalhe.
- [docs/especificacao.md](docs/especificacao.md): decisões de produto.

Não repita aqui o que já está lá; atualize o README/docs quando a mudança afetar o usuário.

## Stack e três modos de execução

- **Backend:** Go 1.25 (`backend/`, módulo `github.com/mtsrodrigues4991/new-app-chaos/backend`), SQLite, SSE para o ao vivo.
  Fala com o Docker pela Engine API via socket (sem SDK do Docker) e com o Kubernetes via API REST
  (`internal/kube`, com `kubefake` para testes).
- **Frontend:** React 18 + TypeScript + Vite + Recharts (`frontend/`). Visual "chaos", laranja-brasa.
- **Modos:** `docker compose up --build` (alvos = containers), `make kind-up` (KIND + Helm + Chaos Mesh,
  alvos = Deployments) e Azure AKS (mesmos charts, com login e Ingress). O modo Docker continua suportado:
  não quebre um modo ao mexer no outro. A abstração fica em `internal/runner/platform.go` (`docker.go`, `k8s.go`).

## Como testar

```bash
make test                       # go vet + go test no backend, tsc --noEmit no frontend
cd backend && go test ./internal/runner/...   # um pacote
```

O CI (`.github/workflows/ci.yml`) roda também `helm lint`/`helm template` dos charts (incluindo os valores
da nuvem), `terraform fmt -check` + `validate` em `infra/azure` e `shellcheck -S warning scripts/*.sh`.
Rode o equivalente localmente quando mexer nessas pastas.

Teste real fica com o Mateus, no WSL dele (veja "Ambiente do Mateus").

## Decisões de arquitetura (não mude sem conversar)

- **Caos no Docker:** injector próprio, não krkn. `tc netem` aplicado por um container auxiliar que usa a
  imagem do próprio backend (`TOOLS_IMAGE`, resolvida pelo **nome** da imagem, não pelo id, porque o id dá
  404 no Docker do WSL); CPU/memória pelo stressor estático `backend/cmd/stressor` copiado para dentro do
  alvo; kill = stop SIGKILL + start. Parâmetros com os **nomes do krkn** (`latency`, `jitter`, `loss`,
  `cpu-load-percentage`, `cpu-workers`, `memory-vm-bytes`...).
- **Caos no Kubernetes:** Chaos Mesh (`internal/chaosmesh`). krkn só no futuro, como motor opcional para nó/zona.
- **Toda falha é revertida** no fim do bloco, ao abortar e na subida seguinte se o backend cair. Uma falha por vez
  (combinar falhas está no backlog).
- **k6 fixado em `grafana/k6:0.54.0`:** o backend lê o dashboard embutido (`K6_WEB_DASHBOARD`, SSE `/events`,
  1 s), formato interno do k6. O k6 1.x não aceita `--no-summary`. Não use `--out json` (trava com APIs rápidas).
  No K8s o k6 roda como Job (`K6_NODE_SELECTOR`/`K6_TOLERATIONS` para o pool spot na Azure).
- **Veredito:** SLOs editáveis + nota 0-100 inspirada no Resiliency Score do krkn (`internal/verdict`),
  sem IA e reproduzível.
- **IA:** provedor trocável (`internal/ai/provider.go`), Claude via Anthropic SDK Go, padrão `claude-opus-5-5`.
  Análise sob demanda e salva, regressão contra a execução de referência (☆) do cenário, chat por execução,
  leitura do código no GitHub (`internal/coderepo`), logs/eventos do K8s e dados do Grafana (`internal/obs`)
  como evidência.
- **Observabilidade opcional** (`OBS=1`): `grafana/otel-lgtm` em um pod, servido pelo próprio app em `/grafana`
  (proxy no backend, `OBS_GRAFANA_PROXY`).
- **Login** (`internal/auth`, ativo com `AUTH_ENABLED`): usuário/senha, todos podem tudo, protege API e `/grafana`.
  No modo nuvem há travas: `ALLOWED_TARGET_HOSTS` e teto diário de IA `AI_DAILY_LIMIT`.

## Limites conhecidos do ambiente

- WSL precisa de `sudo modprobe sch_netem` (já está em `/etc/modules-load.d/` na máquina do Mateus).
- HTTPChaos não injeta no WSL2 (kernel sem `ebtables`); na Azure funciona.
- `kind load` falha no Docker 29 (containerd image store); o `kind-up.sh` já faz fallback com `docker save` + `ctr import`.
- O Grafana usa porta 3000 fixa no `GF_SERVER_ROOT_URL`; `UI_PORT` diferente quebra redirects.

## Azure (MVP plus)

- Assinatura Pay-As-You-Go pessoal, resource group `new-app`, região `eastus2`, AKS Free, ACR, Terraform em
  `infra/azure`, instalação por `scripts/azure-deploy.sh`.
- GitHub Actions entra via **OIDC** (identidade dona só do RG). Workflows: **"Azure - Ligar ambiente"**
  (`azure-up.yml`, ~20 min) e **"Azure - Desligar ambiente"** (`azure-down.yml`).
- VMs da família **DASv4** (DASv5 tem cota 0). Cota regional: 10 vCPU + 3 spot. Pools: `apps` 2× D4as_v4,
  `obs` 1× D2as_v4, `load` 1× D2as_v4 spot (k6).
- URL: https://new-app-chaos-aa7c69.eastus2.cloudapp.azure.com (usuário admin `mateus`).
- Secrets do repositório (só os nomes): `ANTHROPIC_API_KEY`, `ADMIN_PASSWORD`, `CODE_GITHUB_TOKEN`, opcional `APP_USERS`.
- **Sempre DESLIGAR o ambiente depois de testar** (custa ~US$ 0,55/h ligado). O "Desligar" guarda o banco na storage account e o "Ligar" devolve (`scripts/azure-db-backup.sh`).
- Nunca escreva segredos em arquivos, commits ou memória.

## Histórico (marcos e PRs)

| PR | Entrega |
|---|---|
| #1 | Esqueleto (Go + React + compose + demo-api) |
| #2, #3 | Marco 2: k6 + gráficos ao vivo + SQLite; fim de execução e abortar |
| #4, #5, #6 | Marco 3: falhas na linha do tempo; collection Postman; fix da imagem do `tc` |
| #7 | Marco 4: IA (SLOs, nota, análise, regressão, chat, chave em `.env` e Configurações) |
| #8, #9, #10 | Relatório imprimível; IA lê código do GitHub; precisão das citações |
| #11–#16 | MVP2 Kubernetes: A (KIND/Helm/Chaos Mesh) + fix, B (Postgres/IO/DNS/HTTP), C (logs/eventos), D (Grafana opcional) + ajustes |
| #17, #18 | Visual novo laranja-brasa; botão Grafana em `/grafana` |
| #19–#21 | MVP plus Azure: login, chart da nuvem, Terraform, Ligar/Desligar, CI |
| #22 | README novo + docs/funcionalidades.md |

## Próximos passos

- **MVP3: VMs** (carga e caos via SSH). Começar com a entrevista do Mateus.
- Backlog completo no README. Curva de carga desenhável e falhas combinadas **só se ele pedir**.

## Ambiente e preferências do Mateus

- Windows + WSL (Debian), Docker Engine nativo no WSL (sem Docker Desktop). Usa VS Code.
- Clone dele: `~/new-app-chaos-1`. Ferramentas (kind, kubectl, helm) em `~/.local/bin` (ponha no `PATH`).
- Comando usual do KIND: `export PATH="$HOME/.local/bin:$PATH"; OBS=1 PIX_DIR=/mnt/d/Itau/pix ./scripts/kind-up.sh`.
- Conhece bem Java (prefere), e usou krkn em larga escala. Explique Go/React quando não for óbvio.
- Gosta de entregas em fases, um PR por fase, com ele testando entre elas.
- Commits e PRs em português, mensagens curtas descrevendo o efeito ("Fase B: falha que não aplica não atrasa as próximas").
