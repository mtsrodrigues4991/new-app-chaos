# new-app-chaos

> Nome provisório.

App web que une **teste de carga (k6)**, **chaos engineering** e uma **IA que analisa cada execução**: o que aconteceu, onde a API quebra, o que regrediu e o que corrigir.

A especificação completa (visão, escopo do MVP, IA, interface, arquitetura, modelo de dados e roadmap) está em [docs/especificacao.md](docs/especificacao.md).

## Status

Marcos 1 (esqueleto) e 2 (carga) prontos:

- **Alvos:** lista os containers Docker rodando (nome, imagem, portas, rede) e cria um cenário a partir de um deles.
- **Cenários:** editor por formulário com alvo, URL base, endpoints (método, caminho, corpo, peso) e perfil de carga pronto (rampa, patamar ou pico) com prévia dos VUs. Salvos em SQLite.
- **Execução:** o backend sobe um container `grafana/k6` na mesma rede do alvo, lê as métricas em streaming e transmite ao vivo (Server-Sent Events). Dá para abortar. Uma execução por vez.
- **Gráficos ao vivo e histórico:** latência p50/p95/p99, RPS, VUs ativos, respostas por código HTTP, taxa de erro, CPU e memória do container alvo. Tudo agregado por segundo e salvo, então execuções antigas podem ser reabertas.

Próximos: curva de carga desenhável, injeção de falhas na linha do tempo (marco 3) e análise por IA (marco 4).

## Rodando

Requisitos: Docker com Compose.

```bash
docker compose up --build
```

Na primeira execução de um cenário, o backend baixa a imagem `grafana/k6` (pode levar um minuto). Os dados ficam no volume `app-data`; para zerar, `docker compose down -v`.

- Frontend: http://localhost:3000 (comece em Alvos → Criar cenário no `demo-api`)
- Backend: http://localhost:8080/api/health
- API de exemplo: http://localhost:8081/items

### Desenvolvimento sem Docker

```bash
# backend (Go 1.24+)
cd backend && go run ./cmd/server

# frontend (Node 22+)
cd frontend && npm install && npm run dev
```

## Estrutura

```
backend/            API em Go
  cmd/server/       ponto de entrada
  internal/api/     rotas REST e stream ao vivo (SSE)
  internal/dockerx/ cliente da Docker Engine API via socket
  internal/scenario/ modelo: alvo, endpoints, perfil de carga, falhas, SLOs
  internal/load/    geração do script k6 e agregação das métricas
  internal/runner/  orquestra cada execução (k6, stats do alvo, streaming)
  internal/chaos/   injeção e reversão de falhas
  internal/ai/      interface LLMProvider (Claude no MVP)
  internal/store/   persistência em SQLite
frontend/           React + TypeScript + Vite
injector/           imagem com tc e stress-ng para injetar falhas
examples/demo-api/  API de exemplo para testes
docs/               especificação
```

## Roadmap do MVP

1. Esqueleto (feito)
2. Carga: editor de endpoints e perfil, execução do k6, gráficos ao vivo (feito; falta a curva desenhável)
3. Caos: injetor, as cinco falhas, linha do tempo arrastável, reversão garantida
4. IA: agregação, SLOs e veredito, relatório via Claude, comparação, chat

Depois: VMs via SSH, Kubernetes, outros provedores de IA, CLI para CI, SaaS.
