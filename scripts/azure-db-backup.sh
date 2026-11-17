#!/usr/bin/env bash
# Guarda o banco do app (cenários, execuções, experimentos, análises) na storage account, e o
# devolve a um ambiente recém-criado. É o que faz o histórico sobreviver ao "Desligar": o
# cluster e o disco dele são apagados, mas a storage account fica.
#
# Uso: scripts/azure-db-backup.sh backup|restore <storage account>
#      (precisa de kubectl apontando para o AKS, az login e RG)
#
# O backup é uma cópia consistente feita pelo próprio backend, sem pará-lo. Fica em
# appdb/app.db.gz (a mais recente) e appdb/app-<dia>.db.gz (uma por dia, para voltar atrás).
set -euo pipefail
action=$1 sa=$2
NS=new-app-chaos
db="$(mktemp)"
trap 'rm -f "$db" "$db.gz"' EXIT
key=$(az storage account keys list -n "$sa" -g "$RG" --query '[0].value' -o tsv)
blob() { az storage blob "$@" --account-name "$sa" --account-key "$key" -c appdb -o none; }

pod=$(kubectl -n "$NS" get pods -l app.kubernetes.io/name=new-app-chaos-backend \
  --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$pod" ]]; then echo "sem backend rodando no cluster; nada a fazer"; exit 0; fi
# Um backend anterior ao backup não entende "server backup" (subiria outro servidor no pod).
if ! kubectl -n "$NS" exec "$pod" -- test -f /usr/local/lib/new-app-chaos/can-backup 2>/dev/null; then
  echo "aviso: o backend deste ambiente é anterior ao backup do banco; nada foi guardado nem devolvido"; exit 0
fi

case "$action" in
  backup)
    # A cópia fica no /tmp do pod, que some com ele; o próximo backup a sobrescreve.
    kubectl -n "$NS" exec "$pod" -- server backup /tmp/app-backup.db >/dev/null
    kubectl -n "$NS" exec "$pod" -- cat /tmp/app-backup.db > "$db"
    # Um arquivo vazio ou truncado não pode sobrescrever um backup bom.
    if [[ "$(head -c 15 "$db")" != "SQLite format 3" ]]; then
      echo "erro: a cópia do banco não é um arquivo SQLite; o backup anterior foi mantido" >&2; exit 1
    fi
    gzip -9 -k "$db"
    blob upload -n app.db.gz -f "$db.gz" --overwrite
    blob upload -n "app-$(date -u +%F).db.gz" -f "$db.gz" --overwrite
    echo "banco guardado ($(du -h "$db.gz" | cut -f1))"
    ;;
  restore)
    if ! blob download -n app.db.gz -f "$db.gz" 2>/dev/null; then
      echo "sem banco guardado; o ambiente começa vazio"; exit 0
    fi
    gunzip -c "$db.gz" > "$db"
    # O arquivo entra ao lado do banco; o backend o adota ao reiniciar (e o confere antes).
    kubectl -n "$NS" exec -i "$pod" -- sh -c 'cat > /data/app.db.restore' < "$db"
    kubectl -n "$NS" rollout restart deploy/backend >/dev/null
    kubectl -n "$NS" rollout status deploy/backend --timeout 5m >/dev/null
    echo "banco restaurado"
    ;;
  *) echo "uso: $0 backup|restore <storage account>" >&2; exit 1 ;;
esac
