#!/usr/bin/env bash
# Sobe o new-app-chaos num cluster KIND local: cria o cluster, instala metrics-server e
# Chaos Mesh, constrói as imagens (app e alvos), carrega no nó e instala tudo com Helm.
# Pode rodar de novo: reaproveita o cluster e atualiza o que mudou.
#
# Variáveis (todas opcionais):
#   UI_PORT=3000          porta do localhost para a interface (só vale ao criar o cluster)
#   TARGETS="..."         alvos a instalar (padrão: demo-api pix svc-pedido svc-pagamento svc-catalogo)
#   PIX_DIR=caminho       clone local do itau-case-pix (padrão: clona em .targets/)
#   DORIGAO_DIR=caminho   pasta com svc-pedido, svc-pagamento, svc-catalogo (padrão: .targets/)
#   ONLY_APP=1            só reconstrói e reinstala o new-app-chaos (make kind-app)
#   OBS=1                 instala a stack de observabilidade (grafana/otel-lgtm: Prometheus, Tempo,
#                         Loki, Pyroscope e Grafana num pod, ~1-2 GB de RAM) e instrumenta o pix-api
#                         com os agentes OpenTelemetry e Pyroscope; a IA passa a ver JVM, traces e
#                         profiling. Rodar de novo sem OBS=1 desliga a instrumentação.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

CLUSTER=new-app-chaos
NS=new-app-chaos
UI_PORT="${UI_PORT:-3000}"
TARGETS="${TARGETS:-demo-api pix svc-pedido svc-pagamento svc-catalogo}"
TARGETS_DIR="$ROOT/.targets"
PIX_DIR="${PIX_DIR:-$TARGETS_DIR/itau-case-pix}"
DORIGAO_DIR="${DORIGAO_DIR:-$TARGETS_DIR}"
CHAOS_MESH_VERSION="${CHAOS_MESH_VERSION:-2.8.8}"
METRICS_SERVER_VERSION="${METRICS_SERVER_VERSION:-3.13.0}"
K6_IMAGE="${K6_IMAGE:-grafana/k6:0.54.0}"
BUILD_ID="$(date +%s)"
OBS_ON=false; [[ -n "${OBS:-}" && "${OBS:-}" != 0 ]] && OBS_ON=true

step() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31merro:\033[0m %s\n' "$*" >&2; exit 1; }

for tool in docker kind kubectl helm git; do
  command -v "$tool" >/dev/null || fail "$tool não encontrado. Rode 'make kind-tools' para instalar kind, kubectl e helm em ~/.local/bin."
done
docker info >/dev/null 2>&1 || fail "o Docker não está respondendo (no WSL: sudo service docker start)."

build() { # build <imagem> <contexto> [Dockerfile]
  local img=$1 ctx=$2 file=${3:-}
  echo "construindo $img"
  if [[ -n "$file" ]]; then docker build -q -t "$img" -f "$file" "$ctx" >/dev/null
  else docker build -q -t "$img" "$ctx" >/dev/null; fi
}

# Carrega imagens no nó do KIND. O `kind load` falha no Docker 29 com o image store do
# containerd ("content digest ... not found", sobretudo em imagens multiplataforma como a do
# k6). Nesse caso, exporta só a arquitetura do host e importa direto no containerd do nó.
load() {
  local img arch
  arch="$(docker version -f '{{.Server.Arch}}')"
  for img in "$@"; do
    kind load docker-image --name "$CLUSTER" "$img" >/dev/null 2>&1 && continue
    docker save --platform "linux/$arch" "$img" \
      | docker exec -i "$CLUSTER-control-plane" ctr --namespace=k8s.io images import --digests --snapshotter=overlayfs - >/dev/null \
      || fail "não consegui carregar a imagem $img no nó do KIND"
  done
}

clone() { # clone <url> <pasta>
  if [[ ! -d "$2" ]]; then
    echo "clonando $1"
    git clone -q --depth 1 "$1" "$2"
  fi
}

# --- cluster ---
if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  [[ -n "${ONLY_APP:-}" ]] && fail "o cluster não existe; rode make kind-up primeiro."
  if (exec 3<>/dev/tcp/127.0.0.1/"$UI_PORT") 2>/dev/null; then
    fail "a porta $UI_PORT já está em uso (o docker compose do new-app-chaos está no ar?). Pare com 'docker compose down' ou use UI_PORT=3001 make kind-up."
  fi
  step "Criando o cluster KIND $CLUSTER (interface em localhost:$UI_PORT)"
  cfg="$(mktemp)"
  sed "s/hostPort: 3000/hostPort: $UI_PORT/" deploy/kind/cluster.yaml > "$cfg"
  kind create cluster --config "$cfg" --wait 120s
  rm -f "$cfg"
fi
kubectl config use-context "kind-$CLUSTER" >/dev/null

if [[ -z "${ONLY_APP:-}" ]]; then
  step "metrics-server (CPU e memória dos pods)"
  helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ --force-update >/dev/null
  helm repo add chaos-mesh https://charts.chaos-mesh.org --force-update >/dev/null
  helm repo update >/dev/null
  # No KIND o certificado do kubelet é autoassinado.
  helm upgrade --install metrics-server metrics-server/metrics-server -n kube-system \
    --version "$METRICS_SERVER_VERSION" --set 'args={--kubelet-insecure-tls}' --wait --timeout 5m >/dev/null

  step "Chaos Mesh $CHAOS_MESH_VERSION (motor de falhas)"
  # O KIND usa containerd; o dashboard do Chaos Mesh fica de fora (a interface é o new-app-chaos).
  helm upgrade --install chaos-mesh chaos-mesh/chaos-mesh -n chaos-mesh --create-namespace \
    --version "$CHAOS_MESH_VERSION" \
    --set chaosDaemon.runtime=containerd \
    --set chaosDaemon.socketPath=/run/containerd/containerd.sock \
    --set dashboard.create=false \
    --wait --timeout 10m >/dev/null
fi

# --- observabilidade (opcional) ---
if $OBS_ON; then
  step "Stack de observabilidade (Grafana, Prometheus, Tempo, Loki, Pyroscope)"
  kubectl apply -f deploy/observability/lgtm.yaml >/dev/null
  echo "agentes Java (OpenTelemetry e Pyroscope)"
  build new-app-chaos-java-agents:dev deploy/agents
  load new-app-chaos-java-agents:dev
  # Na primeira vez o nó baixa a imagem do otel-lgtm (~1 GB).
  kubectl -n observability rollout status deploy/lgtm --timeout 10m >/dev/null \
    || echo "aviso: a stack de observabilidade não ficou pronta; veja kubectl -n observability describe pod -l app=lgtm"
fi

# --- new-app-chaos ---
step "Imagens do new-app-chaos"
build new-app-chaos-backend:dev backend
build new-app-chaos-frontend:dev frontend
docker image inspect "$K6_IMAGE" >/dev/null 2>&1 || docker pull -q "$K6_IMAGE" >/dev/null
load new-app-chaos-backend:dev new-app-chaos-frontend:dev "$K6_IMAGE"

step "Instalando o new-app-chaos no namespace $NS"
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
# Só as duas chaves saem do .env, por arquivo (nunca na linha de comando).
secret_env="$(mktemp)"; chmod 600 "$secret_env"
trap 'rm -f "$secret_env"' EXIT
if [[ -f .env ]]; then grep -E '^(ANTHROPIC_API_KEY|GITHUB_TOKEN)=.' .env > "$secret_env" || true; fi
kubectl -n "$NS" create secret generic new-app-chaos-secrets --from-env-file="$secret_env" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
[[ -s "$secret_env" ]] || echo "aviso: sem ANTHROPIC_API_KEY no .env; a análise por IA fica desativada até preencher e rodar de novo."
helm upgrade --install new-app-chaos deploy/charts/new-app-chaos -n "$NS" \
  --set buildId="$BUILD_ID" --set backend.k6Image="$K6_IMAGE" --set observability.enabled="$OBS_ON" --wait --timeout 5m >/dev/null

# --- alvos ---
target() { # target <release> <namespace> [args do helm...]
  local rel=$1 ns=$2; shift 2
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  helm upgrade --install "$rel" deploy/charts/target -n "$ns" -f "deploy/targets/$rel.yaml" --set buildId="$BUILD_ID" "$@" >/dev/null
}
# Alvos Java instrumentados pelos agentes quando OBS=1.
java_otel=(--set otel.enabled="$OBS_ON" --set otel.java="$OBS_ON" --set otel.profiling="$OBS_ON")

if [[ -z "${ONLY_APP:-}" ]]; then
  for t in $TARGETS; do
    case "$t" in
      demo-api)
        step "Alvo demo-api (namespace demo)"
        build new-app-chaos-demo-api:dev examples/demo-api
        load new-app-chaos-demo-api:dev
        target demo-api demo
        ;;
      pix)
        step "Alvo itau-case-pix + Postgres (namespace pix)"
        clone https://github.com/mtsrodrigues4991/itau-case-pix "$PIX_DIR"
        build new-app-chaos-pix-api:dev "$PIX_DIR" deploy/targets/pix/Dockerfile
        docker image inspect postgres:16-alpine >/dev/null 2>&1 || docker pull -q postgres:16-alpine >/dev/null
        load new-app-chaos-pix-api:dev postgres:16-alpine
        target pix-postgres pix
        target pix-api pix "${java_otel[@]}"
        ;;
      svc-pedido|svc-pagamento|svc-catalogo)
        step "Alvo $t (namespace dorigao)"
        clone "https://github.com/Dorigao-LTDA/$t" "$DORIGAO_DIR/$t"
        build "new-app-chaos-$t:dev" "$DORIGAO_DIR/$t"
        load "new-app-chaos-$t:dev"
        target "$t" dorigao
        ;;
      *) fail "alvo desconhecido: $t" ;;
    esac
  done

  step "Esperando os alvos ficarem prontos"
  for ns in demo pix dorigao; do
    kubectl get ns "$ns" >/dev/null 2>&1 || continue
    for d in $(kubectl -n "$ns" get deploy -o name); do
      kubectl -n "$ns" rollout status "$d" --timeout 5m || echo "aviso: $ns/${d#*/} não ficou pronto; veja kubectl -n $ns describe ${d}"
    done
  done
fi

# Porta real do host (o cluster pode ter sido criado com outro UI_PORT).
port="$(docker port "$CLUSTER-control-plane" 30080/tcp 2>/dev/null | head -1 | sed 's/.*://')"
step "Pronto: http://localhost:${port:-$UI_PORT}"
echo "Alvos (URL base no cenário): demo-api http://demo-api.demo:8080 · pix http://pix-api.pix:8080 · svc-* http://svc-pedido.dorigao:8080"
if $OBS_ON; then
  echo "Grafana: http://localhost:${port:-$UI_PORT}/grafana/ (botão Grafana na barra lateral do app)"
fi
