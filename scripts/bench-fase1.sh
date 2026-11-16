#!/usr/bin/env bash
# Prova da Fase 1 (docs/analise-evolucao.md, seção 6): um teste A/A dá "sem diferença" e uma
# versão B com atraso artificial dá "piorou". Resultado: docs/resultados/fase-1-experimentos.md.
#
# Sobe um ambiente Docker próprio (rede, duas cópias do demo-api e um backend, com o prefixo
# "nacbench"), sem portas publicadas, e apaga tudo no fim. Não toca no docker compose do app
# nem no KIND. Como todo backend em modo Docker, o daqui remove containers do k6 que sobraram
# de execuções interrompidas ao subir; não rode com uma execução em andamento em outro backend.
#
# Variáveis (opcionais):
#   DELAY_MS=20          atraso artificial da versão B
#   OUT=pasta            onde gravar experimentos.json (padrão: .bench/fase1)
#   BENCH_ARGS="..."     argumentos do scripts/bench-fase1/main.go (ex.: "-duration 60")
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
OUT="${OUT:-$ROOT/.bench/fase1}"
NET=nacbench
DELAY_MS="${DELAY_MS:-20}"
K6_IMAGE="${K6_IMAGE:-grafana/k6:0.54.0}"
mkdir -p "$OUT"

cleanup() {
  docker rm -f nacbench-after nacbench-demo-api nacbench-demo-api-b >/dev/null 2>&1 || true
  docker ps -aq --filter "network=$NET" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

echo "==> imagens"
docker build -q -t nacbench-backend:after backend >/dev/null
docker build -q -t nacbench-demo-api examples/demo-api >/dev/null
docker image inspect "$K6_IMAGE" >/dev/null 2>&1 || docker pull -q "$K6_IMAGE" >/dev/null

echo "==> ambiente isolado (rede $NET; B com ${DELAY_MS} ms de atraso)"
docker network create "$NET" >/dev/null
# A mesma imagem dos dois lados: a única diferença entre A e B é o atraso.
docker run -d --name nacbench-demo-api --network "$NET" --cpus 2 nacbench-demo-api >/dev/null
docker run -d --name nacbench-demo-api-b --network "$NET" --cpus 2 -e DELAY_MS="$DELAY_MS" nacbench-demo-api >/dev/null
docker run -d --name nacbench-after --network "$NET" -v /var/run/docker.sock:/var/run/docker.sock \
  -e DB_PATH=/data/app.db -e K6_IMAGE="$K6_IMAGE" nacbench-backend:after >/dev/null
sleep 3

echo "==> experimentos (a saída fica também em $OUT)"
# shellcheck disable=SC2086 # BENCH_ARGS são vários argumentos
docker run --rm --network "$NET" -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
  -v "$ROOT/scripts/bench-fase1:/src:ro" -v "$OUT:/out" golang:1.25 go run /src/main.go ${BENCH_ARGS:-} | tee "$OUT/saida.txt"
