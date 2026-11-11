# new-app-chaos

> Nome provisório.

App web que une **teste de carga (k6)**, **chaos engineering** e uma **IA que analisa cada execução**: o que aconteceu, onde a API quebra, o que regrediu e o que corrigir.

A especificação completa (visão, escopo do MVP, IA, interface, arquitetura, modelo de dados e roadmap) está em [docs/especificacao.md](docs/especificacao.md).

## Status

Marcos 1 (esqueleto), 2 (carga), 3 (caos) e 4 (IA) prontos, e a fase A do Kubernetes:

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
  - **Código da aplicação:** no cenário, informe o repositório no GitHub (`dono/repo` ou URL), a branch e, se quiser, uma subpasta. Na análise e no chat a IA baixa essa branch (fica em cache por commit), navega no código só com leitura (listar pastas, buscar, abrir até 30 arquivos por análise) e as correções apontam arquivo e linha com link para o GitHub e o trecho proposto. O relatório registra o commit lido. Repositório privado precisa de `GITHUB_TOKEN` no `.env`. Sem repositório, nada muda.
  - **Relatório imprimível:** o botão "Imprimir / salvar PDF" na execução gera um relatório em A4, sempre em tema claro, com números, veredito, falhas, gráficos e a análise da IA (sem o chat).
  - **Configurações:** escolhe provedor e modelo (padrão Claude Opus 5.5; Sonnet 5.5 e Haiku 5.5 são mais baratos). A camada de provedor (`backend/internal/ai`) é trocável.

- **Kubernetes (MVP2, fase A):** o app também roda dentro de um cluster KIND local (`make kind-up`). Lá o alvo é um **Deployment** (namespace + nome) em vez de um container:
  - **Carga:** o k6 roda como um Job no namespace do app (`nac-k6-<execução>`); o backend lê o dashboard do k6 pelo IP do pod.
  - **Falhas pelo Chaos Mesh:** latência e perda viram `NetworkChaos`, CPU e memória viram `StressChaos` e o kill vira `PodChaos pod-kill`. Cada falha tem a opção **um pod (sorteado) ou todos os pods**; o padrão é todos, e no kill é um. O recurso é criado no início do bloco, o app espera o Chaos Mesh confirmar a injeção e apaga no fim. O `duration` do recurso é o bloco + 30 s, uma rede de segurança se o backend cair.
  - **Recursos:** CPU e memória somadas de todos os pods do Deployment (metrics-server, que atualiza a cada ~15 s) e um gráfico de **pods prontos x existentes**, que mostra a recuperação depois de um kill. A IA recebe as mesmas colunas.
  - No kill do Kubernetes o pod é apagado no início do bloco e o Deployment recria sozinho; o bloco é o tempo em que a recuperação é observada (no Docker o container fica parado até o fim do bloco).
  - **Falhas em dependências (fase B):** cada falha tem "Aplicar em": o alvo ou outro Deployment do cluster usado por ele (ex.: `pix/pix-postgres`). A carga continua no alvo e o veredito mede o efeito nele. Latência e perda podem ficar **só no caminho do alvo até a dependência** (NetworkChaos com `target` e `direction: both`, porque o alvo fala com a dependência pelo IP do Service e a regra só casa o IP dos pods na volta): o resto do tráfego segue normal.
  - **Falhas só do Kubernetes (fase B):**
    - **Atraso de disco** e **erro de disco** (`IOChaos`): num volume do container (`volume-path`, ex.: `/var/lib/postgresql/data`; o Postgres do pix já sobe com esse volume), com `delay`, `errno` (5 = EIO) e `percent` das operações. Só aparece em operações que vão ao disco: um `SELECT` que o banco já tem em cache não sente, então use carga com escritas. O Postgres do pix guarda os dados num PVC, que sobrevive ao kill do pod.
    - **Erro de DNS** (`DNSChaos`): a resolução de nomes falha para os domínios em `patterns` (ex.: `pix-postgres.*`; vazio = todos). Só afeta conexões novas: um pool já aberto (ex.: Hikari com o banco) continua funcionando.
    - **Atraso HTTP** e **aborto HTTP** (`HTTPChaos`): nas requisições que chegam aos pods, filtradas por `port`, `path` e `method`. Não vale para HTTPS. Exigem o módulo `ebtables` no kernel dos nós, que o **WSL2 não tem**: no KIND sobre o WSL2 elas não são aplicadas.
    - As cores seguem o efeito: atrasos usam a cor da latência e erros a da perda; o rótulo do bloco diz qual é a falha e, se for numa dependência, onde (`@ pix-postgres`, ou `→ pix-postgres` só no caminho).
  - **Falha que não confirma:** se o Chaos Mesh não confirma a injeção em 45 s (ou até o fim do bloco, se ele for mais curto), a falha fica marcada como "falhou" (com o erro do Chaos Mesh) e a execução segue com as próximas. O recurso é apagado em segundo plano, sem atrasar as próximas falhas, e, se o Chaos Mesh não o liberar, o app tira o finalizer dele para não deixar nada preso no cluster.
  - **Logs e eventos na análise (fase C):** durante a execução o app coleta o log dos containers do alvo e das dependências usadas nas falhas (um stream do log de cada container, aberto assim que o pod aparece e mantido até ele terminar, então um pod apagado pelo kill fica com as linhas até o fim; a instância anterior de um container que reiniciou também entra) e, no fim, os eventos do Kubernetes desses namespaces (Killing, Unhealthy, BackOff...; do Chaos Mesh só Applied, Recovered e erros). Os eventos têm precisão de 1 s, então podem aparecer até ~1 s antes. No Docker entra o log do container alvo. Tudo fica salvo com a execução, aparece em "Logs e eventos" na tela dela (com a falha ativa em cada linha) e vai para a IA resumido: linhas iguais a menos de números juntadas, prioridade para erros, conexões, partidas e paradas, até ~40 mil caracteres.
  - O modo Docker não mudou: `docker compose up` continua igual.

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

Para a IA ler o código de um repositório **privado**, acrescente no mesmo `.env` um `GITHUB_TOKEN` fine-grained só com *Contents: Read-only* nos repositórios desejados. Públicos funcionam sem token.

As chaves ficam só no backend: não vão para o banco nem para o navegador. Sem ela, todo o resto funciona e o botão da IA explica o que falta. Cada análise ou pergunta no chat consome créditos da sua conta (uma execução de 1 minuto manda uns 6 mil tokens; as longas são agrupadas em blocos e ficam por volta de 20 mil).

Na primeira execução de um cenário, o backend baixa a imagem `grafana/k6` (pode levar um minuto). Os dados ficam no volume `app-data`; para zerar, `docker compose down -v`.

As falhas de rede usam o módulo `netem` do kernel. A maioria das distribuições já o carrega sob demanda; se aparecer o erro "o kernel desta máquina não tem o módulo netem", rode `sudo modprobe sch_netem`.

- Frontend: http://localhost:3000 (comece em Alvos → Criar cenário no `demo-api`)
- Backend: http://localhost:8080/api/health
- API de exemplo: http://localhost:8081/items (collection do Postman com `/health`, `/items` e `/compute` em [examples/demo-api/postman_collection.json](examples/demo-api/postman_collection.json))

### Kubernetes local (KIND)

Requisitos: Docker, `make` (`sudo apt install make` no Debian/WSL), `git` e as ferramentas kind, kubectl e helm (`make kind-tools` instala as três em `~/.local/bin`). Reserve uns 6 GB de RAM para o cluster com todos os alvos; no WSL, ajuste `memory=` no `.wslconfig` se precisar.

```bash
make kind-up      # cria o cluster e instala metrics-server, Chaos Mesh, o new-app-chaos e os alvos
make kind-app     # depois de mudar o código: reconstrói e reinstala só o new-app-chaos
make kind-down    # apaga o cluster (e o banco do app que estava nele)
```

- Interface em http://localhost:3000. A porta é a mesma do `docker compose`: pare o compose antes (`docker compose down`) ou use `UI_PORT=3001 make kind-up`.
- O `.env` é lido pelo script: `ANTHROPIC_API_KEY` e `GITHUB_TOKEN` viram um Secret do Kubernetes (só essas duas chaves saem do arquivo).
- Alvos instalados (cada um com 2 réplicas, menos o Postgres), com a URL base para usar no cenário:

| Alvo | Namespace | URL base | Origem |
|---|---|---|---|
| demo-api | `demo` | `http://demo-api.demo:8080` | `examples/demo-api` |
| pix-api + pix-postgres | `pix` | `http://pix-api.pix:8080` | [itau-case-pix](https://github.com/mtsrodrigues4991/itau-case-pix), com o Dockerfile de `deploy/targets/pix` |
| svc-pedido, svc-pagamento, svc-catalogo | `dorigao` | `http://svc-pedido.dorigao:8080` (`/api/pedido`, `/api/pagamento`, `/api/catalogo`) | repositórios da Dorigao-LTDA, com os Dockerfiles deles |

- Os repositórios dos alvos são clonados em `.targets/` (fora do git). Para usar um clone que você já tem: `PIX_DIR=~/itau-case-pix make kind-up`. Para instalar só alguns: `TARGETS="demo-api pix" make kind-up`.
- As imagens são construídas na sua máquina e carregadas no nó do KIND (`kind load`); nada vai para registry.
- Estrutura: `deploy/kind/cluster.yaml`, `deploy/charts/new-app-chaos` (backend, frontend, RBAC, volume do SQLite), `deploy/charts/target` (chart genérico dos alvos) e `deploy/targets/*.yaml` (valores de cada alvo).
- Para conferir uma falha por fora: `kubectl get networkchaos,stresschaos,podchaos -A` durante a execução; os Jobs do k6 ficam em `kubectl -n new-app-chaos get jobs`.

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
  internal/kube/    cliente da API do Kubernetes (sem client-go) e kubefake, uma API falsa para testes
  internal/chaosmesh/ tradução das falhas em recursos do Chaos Mesh
  internal/scenario/ modelo: alvo, endpoints, perfil de carga, falhas, SLOs
  internal/load/    geração do script k6 e agregação das métricas
  internal/runner/  orquestra cada execução (k6, sonda, falhas, stats do alvo, streaming); plataformas Docker e Kubernetes
  internal/chaos/   injeção e reversão de falhas (tc netem, stressor, kill/restart)
  internal/probe/   sonda HTTP do modo só caos
  internal/verdict/ SLOs por segundo e nota de resiliência (sem IA)
  internal/analysis/ evidências para a IA, relatório e chat
  internal/coderepo/ download do código da aplicação no GitHub e ferramentas de leitura para a IA
  internal/ai/      camada de provedor de IA trocável (Anthropic/Claude no MVP)
  internal/store/   persistência em SQLite
frontend/           React + TypeScript + Vite
examples/demo-api/  API de exemplo para testes
deploy/             KIND, charts Helm e valores dos alvos
scripts/            kind-up, kind-down e instalação das ferramentas
docs/               especificação
```

## Roadmap do MVP

1. Esqueleto (feito)
2. Carga: editor de endpoints e perfil, execução do k6, gráficos ao vivo (feito; falta a curva desenhável)
3. Caos: injetor, as cinco falhas, linha do tempo arrastável, reversão garantida (feito)
4. IA: SLOs e veredito, relatório via Claude sob demanda, comparação com referência, chat (feito); relatório imprimível e leitura do código da aplicação (feito)

MVP2 (Kubernetes): A, KIND + Helm + alvos Deployment + as 5 falhas pelo Chaos Mesh (feito); B, falha numa dependência (ex.: Postgres) e falhas de IO, DNS e HTTP (feito); C, logs dos pods e eventos do Kubernetes na IA; D, stack Grafana opcional (Prometheus/Mimir, Loki, Tempo, Pyroscope).

Depois: nuvem (AKS/EKS), VMs via SSH, outros provedores de IA, CLI para CI, SaaS.
