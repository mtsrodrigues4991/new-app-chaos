# new-app-chaos

> Nome provisório.

App web que une **teste de carga (k6)**, **chaos engineering** e uma **IA que analisa cada execução**: o que aconteceu, onde a API quebra, o que regrediu e o que corrigir.

A especificação completa (visão, escopo do MVP, IA, interface, arquitetura, modelo de dados e roadmap) está em [docs/especificacao.md](docs/especificacao.md).

## Status

Esqueleto do projeto (marco 1). Já funciona:

- Backend Go com `GET /api/health`, `GET /api/containers` (lista containers Docker em execução) e `GET/POST /api/scenarios` (em memória).
- Frontend React com a navegação das quatro telas e a tela de **Alvos** listando os containers.
- Modelo de cenário, geração do script k6 e tradução das falhas em comandos `tc`/`stress-ng` (com testes).
- Imagem do injetor de falhas e uma API de exemplo (`demo-api`) para testar.

Ainda não implementado: execução do k6, injeção de falhas na linha do tempo, gráficos ao vivo, SQLite e a análise por IA.

## Rodando

Requisitos: Docker com Compose.

```bash
docker compose up --build
```

- Frontend: http://localhost:3000
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
  internal/api/     rotas REST (e WebSocket no futuro)
  internal/dockerx/ cliente da Docker Engine API via socket
  internal/scenario/ modelo: alvo, endpoints, perfil de carga, falhas, SLOs
  internal/load/    geração do script k6
  internal/chaos/   injeção e reversão de falhas
  internal/ai/      interface LLMProvider (Claude no MVP)
  internal/store/   persistência (memória agora, SQLite depois)
frontend/           React + TypeScript + Vite
injector/           imagem com tc e stress-ng para injetar falhas
examples/demo-api/  API de exemplo para testes
docs/               especificação
```

## Roadmap do MVP

1. Esqueleto (este)
2. Carga: editor de endpoints e perfil, execução do k6, gráficos ao vivo
3. Caos: injetor, as cinco falhas, linha do tempo arrastável, reversão garantida
4. IA: agregação, SLOs e veredito, relatório via Claude, comparação, chat

Depois: VMs via SSH, Kubernetes, outros provedores de IA, CLI para CI, SaaS.
