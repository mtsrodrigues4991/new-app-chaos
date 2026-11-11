#!/usr/bin/env bash
# Prepara a assinatura para o GitHub Actions do new-app-chaos subir o MVP plus sozinho.
# Rode UMA vez, no Azure Cloud Shell (portal.azure.com, ícone >_ , modo Bash) ou no WSL com az login feito.
set -euo pipefail

SUB=488eeea4-6eba-44ae-ba31-a4b8a95b7f69   # Pay-As-You-Go
RG=new-app
REPO=mtsrodrigues4991/new-app-chaos
REGION=eastus2

az account set -s "$SUB"

echo "==> Registrando os provedores que o AKS usa (leva 1-2 min)"
for p in Microsoft.ContainerService Microsoft.ContainerRegistry Microsoft.Network Microsoft.Compute \
         Microsoft.Storage Microsoft.ManagedIdentity Microsoft.OperationalInsights Microsoft.Consumption; do
  az provider register -n "$p" >/dev/null
done

echo "==> Identidade do GitHub Actions (login sem senha, por OIDC)"
APP_ID=$(az ad app list --display-name new-app-chaos-github --query '[0].appId' -o tsv)
if [[ -z "$APP_ID" ]]; then
  APP_ID=$(az ad app create --display-name new-app-chaos-github --query appId -o tsv)
fi
az ad sp show --id "$APP_ID" >/dev/null 2>&1 || az ad sp create --id "$APP_ID" >/dev/null
for ref in main; do
  az ad app federated-credential create --id "$APP_ID" --parameters "{\"name\":\"gh-$ref\",\"issuer\":\"https://token.actions.githubusercontent.com\",\"subject\":\"repo:$REPO:ref:refs/heads/$ref\",\"audiences\":[\"api://AzureADTokenExchange\"]}" >/dev/null 2>&1 || true
done

echo "==> Permissão de dono SÓ no resource group $RG"
RG_ID=$(az group show -n "$RG" --query id -o tsv)
sleep 20  # a identidade nova demora um pouco para aparecer
az role assignment create --assignee "$APP_ID" --role Owner --scope "$RG_ID" >/dev/null 2>&1 || echo "(permissão já existia)"

echo
echo "==> Cota de vCPU em $REGION (precisa de 10 normais da família DASv5 e 2 spot)"
az vm list-usage -l "$REGION" -o table | grep -Ei 'Total Regional vCPUs|DASv5|Low-priority' || true

echo
echo "================ COLE ISTO NO CHAT DO CLAUDE ================"
echo "AZURE_CLIENT_ID=$APP_ID"
echo "(e a tabela de cota acima)"
