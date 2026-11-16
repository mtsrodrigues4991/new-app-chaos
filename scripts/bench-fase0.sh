#!/usr/bin/env bash
# Benchmark da Fase 0 (docs/analise-evolucao.md, seção 6): a mesma carga entrega o mesmo RPS
# com e sem latência injetada? Compara o backend de um commit anterior (BEFORE_REF) com o do
# diretório de trabalho. Resultado e metodologia: docs/resultados/fase-0-medicao-confiavel.md.
#
# Sobe um ambiente Docker próprio (rede, alvo e dois backends com o prefixo "nacbench"), sem
# portas publicadas, e apaga tudo no fim. Não toca no docker compose do app nem no KIND.
# Atenção: como todo backend em modo Docker, os daqui removem containers do k6 que sobraram de
# execuções interrompidas ao subir; não rode com uma execução em andamento em outro backend.
#
# Variáveis (opcionais):
#   BEFORE_REF=4ace3b7   commit do "antes"
#   OUT=pasta            onde gravar execucoes.json e tabelas.md (padrão: .bench/fase0)
#   BENCH_ARGS="..."     argumentos do scripts/bench-fase0/main.go (ex.: "-reps 3 -duration 45")
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BEFORE_REF="${BEFORE_REF:-4ace3b7}"
OUT="${OUT:-$ROOT/.bench/fase0}"
NET=nacbench
K6_IMAGE="${K6_IMAGE:-grafana/k6:0.54.0}"

lsmod | grep -q '^sch_netem' || { echo "erro: falta o módulo sch_netem (sudo modprobe sch_netem)" >&2; exit 1; }
mkdir -p "$OUT"
src="$(mktemp -d)"

cleanup() {
  docker rm -f nacbench-before nacbench-after nacbench-demo-api >/dev/null 2>&1 || true
  # k6 e auxiliares de rede criados pelos backends do benchmark (só os desta rede).
  docker ps -aq --filter "network=$NET" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  rm -rf "$src"
}
trap cleanup EXIT
cleanup; mkdir -p "$src"

echo "==> imagens (antes: $BEFORE_REF; depois: diretório de trabalho)"
git archive "$BEFORE_REF" backend | tar -x -C "$src"
docker build -q -t nacbench-backend:before "$src/backend" >/dev/null
docker build -q -t nacbench-backend:after backend >/dev/null
# O commit do alvo vai no rótulo OCI da imagem, que a execução registra (só com a pasta limpa).
labels=()
if [[ -z "$(git status --porcelain -- examples/demo-api)" ]]; then
  labels=(--label "org.opencontainers.image.revision=$(git rev-parse HEAD)")
fi
docker build -q -t nacbench-demo-api "${labels[@]}" examples/demo-api >/dev/null
docker image inspect "$K6_IMAGE" >/dev/null 2>&1 || docker pull -q "$K6_IMAGE" >/dev/null

echo "==> ambiente isolado (rede $NET)"
docker network create "$NET" >/dev/null
# CPU fixa no alvo: a capacidade dele não varia com o que mais roda na máquina.
docker run -d --name nacbench-demo-api --network "$NET" --cpus 2 nacbench-demo-api >/dev/null
for v in before after; do
  docker run -d --name "nacbench-$v" --network "$NET" -v /var/run/docker.sock:/var/run/docker.sock \
    -e DB_PATH=/data/app.db -e K6_IMAGE="$K6_IMAGE" "nacbench-backend:$v" >/dev/null
done
sleep 3

echo "==> execuções (a saída fica também em $OUT)"
# shellcheck disable=SC2086 # BENCH_ARGS são vários argumentos
docker run --rm --network "$NET" -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
  -v "$ROOT/scripts/bench-fase0:/src:ro" -v "$OUT:/out" golang:1.25 go run /src/main.go ${BENCH_ARGS:-} | tee "$OUT/saida.txt"
