#!/usr/bin/env bash
# Cria (se faltar) a storage account do estado do Terraform e do certificado HTTPS guardado.
# Imprime sa=<nome> (para o $GITHUB_OUTPUT). Precisa de az login, RG, LOCATION e AZURE_SUBSCRIPTION_ID.
set -euo pipefail
sa="nacstate$(printf '%s' "$AZURE_SUBSCRIPTION_ID" | sha1sum | cut -c1-6)"
if ! az storage account show -n "$sa" -g "$RG" -o none 2>/dev/null; then
  az storage account create -n "$sa" -g "$RG" -l "$LOCATION" --sku Standard_LRS --kind StorageV2 \
    --min-tls-version TLS1_2 --allow-blob-public-access false -o none >&2
fi
key=$(az storage account keys list -n "$sa" -g "$RG" --query '[0].value' -o tsv)
for c in tfstate tls; do
  az storage container create -n "$c" --account-name "$sa" --account-key "$key" -o none >&2
done
echo "sa=$sa"
