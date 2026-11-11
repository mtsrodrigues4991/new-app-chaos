# new-app-chaos

> Nome provisório.

App web que une **teste de carga (k6)**, **chaos engineering** e uma **IA que analisa cada execução**: o que aconteceu, onde a API quebra, o que regrediu e o que corrigir.

A especificação completa (visão, escopo do MVP, IA, interface, arquitetura, modelo de dados e roadmap) está em [docs/especificacao.md](docs/especificacao.md).

## Status

Marcos 1 (esqueleto), 2 (carga) e 3 (caos) prontos:

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
  - As faixas das falhas aparecem atrás de todos os gráficos, e os instantes reais de aplicação e reversão ficam salvos com a execução (para a IA do marco 4).

Próximos: análise por IA (marco 4). Backlog: curva de carga desenhável, falhas combinadas, banda/DNS/disco, falhas na entrada (ingress).

## Rodando

Requisitos: Docker com Compose.

```bash
docker compose up --build
```

Na primeira execução de um cenário, o backend baixa a imagem `grafana/k6` (pode levar um minuto). Os dados ficam no volume `app-data`; para zerar, `docker compose down -v`.

As falhas de rede usam o módulo `netem` do kernel. A maioria das distribuições já o carrega sob demanda; se aparecer o erro "o kernel desta máquina não tem o módulo netem", rode `sudo modprobe sch_netem`.

- Frontend: http://localhost:3000 (comece em Alvos → Criar cenário no `demo-api`)
- Backend: http://localhost:8080/api/health
- API de exemplo: http://localhost:8081/items

### Desenvolvimento sem Docker

```bash
# backend (Go 1.24+). Para as falhas: compile o stressor e aponte uma imagem com tc
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
  internal/ai/      interface LLMProvider (Claude no MVP)
  internal/store/   persistência em SQLite
frontend/           React + TypeScript + Vite
examples/demo-api/  API de exemplo para testes
docs/               especificação
```

## Roadmap do MVP

1. Esqueleto (feito)
2. Carga: editor de endpoints e perfil, execução do k6, gráficos ao vivo (feito; falta a curva desenhável)
3. Caos: injetor, as cinco falhas, linha do tempo arrastável, reversão garantida (feito)
4. IA: agregação, SLOs e veredito, relatório via Claude, comparação, chat

Depois: VMs via SSH, Kubernetes, outros provedores de IA, CLI para CI, SaaS.
