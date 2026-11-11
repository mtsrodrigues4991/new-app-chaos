#!/usr/bin/env bash
# Instala o new-app-chaos no AKS (MVP plus): Traefik + cert-manager (HTTPS), Chaos Mesh,
# stack de observabilidade, o app com login e os alvos. Mesma lógica do kind-up.sh, com as
# imagens do ACR. Roda no workflow "Ligar" e também no WSL (com az aks get-credentials feito).
#
# Variáveis obrigatórias:
#   ACR=<login server>  TAG=<tag das imagens>  FQDN=<host público>  PIP_NAME=<IP público>  RG=<resource group>
#   ADMIN_PASSWORD      senha do usuário admin (ADMIN_USER, padrão mateus)
# Opcionais: ANTHROPIC_API_KEY, GITHUB_TOKEN, APP_USERS, OBS=1 (padrão), TARGETS, ACME_EMAIL,
#   TLS_BACKUP=<arquivo yaml do certificado guardado> para não pedir outro ao Let's Encrypt.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

: "${ACR:?}" "${TAG:?}" "${FQDN:?}" "${PIP_NAME:?}" "${RG:?}" "${ADMIN_PASSWORD:?defina ADMIN_PASSWORD}"
NS=new-app-chaos
ADMIN_USER="${ADMIN_USER:-mateus}"
TARGETS="${TARGETS:-demo-api pix svc-pedido svc-pagamento svc-catalogo}"
OBS="${OBS:-1}"; OBS_ON=false; [[ "$OBS" != 0 && -n "$OBS" ]] && OBS_ON=true
CHAOS_MESH_VERSION="${CHAOS_MESH_VERSION:-2.8.8}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.2}"
TRAEFIK_VERSION="${TRAEFIK_VERSION:-33.2.1}"
K6_IMAGE="${K6_IMAGE:-grafana/k6:0.54.0}"
BUILD_ID="$(date +%s)"

step() { printf '\n==> %s\n' "$*"; }
ns() { kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f - >/dev/null; }

helm repo add chaos-mesh https://charts.chaos-mesh.org --force-update >/dev/null
helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
helm repo add traefik https://traefik.github.io/charts --force-update >/dev/null
helm repo update >/dev/null

step "Traefik (entrada pública no IP fixo $PIP_NAME)"
# externalTrafficPolicy=Local preserva o IP do usuário (limite de tentativas de login).
cat > /tmp/traefik-values.yaml <<YAML
service:
  annotations:
    service.beta.kubernetes.io/azure-load-balancer-resource-group: "$RG"
    service.beta.kubernetes.io/azure-pip-name: "$PIP_NAME"
    service.beta.kubernetes.io/azure-load-balancer-health-probe-request-path: /ping
  spec:
    externalTrafficPolicy: Local
ingressClass:
  enabled: true
  isDefaultClass: true
providers:
  kubernetesIngress:
    publishedService:
      enabled: true
YAML
helm upgrade --install traefik traefik/traefik -n traefik --create-namespace --version "$TRAEFIK_VERSION" \
  -f /tmp/traefik-values.yaml --wait --timeout 10m >/dev/null

step "cert-manager (certificado HTTPS do Let's Encrypt)"
helm upgrade --install cert-manager jetstack/cert-manager -n cert-manager --create-namespace --version "$CERT_MANAGER_VERSION" \
  --set crds.enabled=true --wait --timeout 10m >/dev/null
kubectl apply -f - >/dev/null <<YAML
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    ${ACME_EMAIL:+email: $ACME_EMAIL}
    privateKeySecretRef:
      name: letsencrypt-account
    solvers:
      - http01:
          ingress:
            ingressClassName: traefik
YAML

step "Chaos Mesh $CHAOS_MESH_VERSION"
helm upgrade --install chaos-mesh chaos-mesh/chaos-mesh -n chaos-mesh --create-namespace --version "$CHAOS_MESH_VERSION" \
  --set chaosDaemon.runtime=containerd \
  --set chaosDaemon.socketPath=/run/containerd/containerd.sock \
  --set controllerManager.replicaCount=1 \
  --set dashboard.create=false \
  --wait --timeout 10m >/dev/null

if $OBS_ON; then
  step "Observabilidade (otel-lgtm no pool obs)"
  kubectl apply -f deploy/observability/lgtm.yaml >/dev/null
  # No pool próprio (taint pool=obs) e longe dos alvos.
  kubectl -n observability patch deployment lgtm --type merge -p \
    '{"spec":{"template":{"spec":{"nodeSelector":{"pool":"obs"},"tolerations":[{"key":"pool","operator":"Equal","value":"obs","effect":"NoSchedule"}]}}}}' >/dev/null
fi

step "new-app-chaos (namespace $NS)"
ns "$NS"
secret_env="$(mktemp)"; chmod 600 "$secret_env"; trap 'rm -f "$secret_env"' EXIT
{
  printf 'ADMIN_PASSWORD=%s\n' "$ADMIN_PASSWORD"
  [[ -n "${ANTHROPIC_API_KEY:-}" ]] && printf 'ANTHROPIC_API_KEY=%s\n' "$ANTHROPIC_API_KEY"
  [[ -n "${GITHUB_TOKEN:-}" ]] && printf 'GITHUB_TOKEN=%s\n' "$GITHUB_TOKEN"
  # APP_USERS pode ter várias linhas: no env-file vira uma linha com vírgulas.
  [[ -n "${APP_USERS:-}" ]] && printf 'APP_USERS=%s\n' "$(printf '%s' "$APP_USERS" | tr '\n' ',' | sed 's/,$//')"
} > "$secret_env"
kubectl -n "$NS" create secret generic new-app-chaos-secrets --from-env-file="$secret_env" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

if [[ -n "${TLS_BACKUP:-}" && -s "$TLS_BACKUP" ]]; then
  echo "devolvendo o certificado HTTPS guardado"
  kubectl -n "$NS" apply -f "$TLS_BACKUP" >/dev/null || echo "aviso: não consegui devolver o certificado; o cert-manager pede outro"
fi

helm upgrade --install new-app-chaos deploy/charts/new-app-chaos -n "$NS" \
  --set buildId="$BUILD_ID" \
  --set backend.image="$ACR/new-app-chaos-backend:$TAG" --set backend.pullPolicy=IfNotPresent \
  --set frontend.image="$ACR/new-app-chaos-frontend:$TAG" --set frontend.pullPolicy=IfNotPresent \
  --set frontend.serviceType=ClusterIP \
  --set backend.k6Image="$K6_IMAGE" \
  --set backend.k6NodeSelector=agentpool=load \
  --set backend.k6Tolerations='kubernetes.azure.com/scalesetpriority=spot:NoSchedule\,pool=load:NoSchedule' \
  --set observability.enabled="$OBS_ON" \
  --set ingress.enabled=true --set ingress.host="$FQDN" \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set auth.enabled=true --set auth.adminUser="$ADMIN_USER" \
  --set guard.allowedTargetHosts='demo\,pix\,dorigao' \
  --set guard.aiDailyLimit="${AI_DAILY_LIMIT:-60}" \
  --wait --timeout 10m >/dev/null

target() { # target <release> <namespace> [args do helm...]
  local rel=$1 n=$2; shift 2
  ns "$n"
  helm upgrade --install "$rel" deploy/charts/target -n "$n" -f "deploy/targets/$rel.yaml" --set buildId="$BUILD_ID" "$@" >/dev/null
}
java_otel=(--set otel.enabled="$OBS_ON" --set otel.java="$OBS_ON" --set otel.profiling="$OBS_ON" --set otel.agentsImage="$ACR/new-app-chaos-java-agents:$TAG")

for t in $TARGETS; do
  case "$t" in
    demo-api) step "Alvo demo-api"; target demo-api demo --set image="$ACR/new-app-chaos-demo-api:$TAG" ;;
    pix)
      step "Alvo pix-api + Postgres"
      target pix-postgres pix
      target pix-api pix --set image="$ACR/new-app-chaos-pix-api:$TAG" "${java_otel[@]}"
      ;;
    svc-pedido|svc-pagamento|svc-catalogo) step "Alvo $t"; target "$t" dorigao --set image="$ACR/new-app-chaos-$t:$TAG" ;;
    *) echo "alvo desconhecido: $t" >&2; exit 1 ;;
  esac
done

step "Esperando tudo ficar pronto"
for n in demo pix dorigao observability; do
  kubectl get ns "$n" >/dev/null 2>&1 || continue
  for d in $(kubectl -n "$n" get deploy -o name); do
    kubectl -n "$n" rollout status "$d" --timeout 10m || echo "aviso: $n/${d#*/} não ficou pronto"
  done
done

step "Certificado HTTPS"
kubectl -n "$NS" wait certificate/new-app-chaos-tls --for=condition=Ready --timeout=8m \
  || echo "aviso: o certificado ainda não ficou pronto; veja kubectl -n $NS describe certificate"

echo
echo "Pronto: https://$FQDN (usuário $ADMIN_USER)"
