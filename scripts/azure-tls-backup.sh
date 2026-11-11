#!/usr/bin/env bash
# Guarda o certificado HTTPS do cluster na storage account (container tls), para o próximo
# "Ligar" devolver em vez de pedir outro ao Let's Encrypt (limite de 5 iguais por semana).
# Uso: scripts/azure-tls-backup.sh <storage account>   (precisa de kubectl apontando para o AKS)
set -euo pipefail
sa=$1
if ! kubectl -n new-app-chaos get secret new-app-chaos-tls -o json > /tmp/tls.json 2>/dev/null; then
  echo "sem certificado no cluster para guardar"; exit 0
fi
if [[ "$(jq -r '.data["tls.crt"] // ""' /tmp/tls.json)" == "" ]]; then
  echo "certificado ainda não emitido; nada a guardar"; exit 0
fi
jq 'del(.metadata.uid, .metadata.resourceVersion, .metadata.creationTimestamp, .metadata.managedFields, .metadata.ownerReferences)' /tmp/tls.json > /tmp/tls-clean.json
key=$(az storage account keys list -n "$sa" -g "$RG" --query '[0].value' -o tsv)
az storage blob upload --account-name "$sa" --account-key "$key" -c tls -n new-app-chaos-tls.yaml -f /tmp/tls-clean.json --overwrite -o none
rm -f /tmp/tls.json /tmp/tls-clean.json
echo "certificado guardado"
