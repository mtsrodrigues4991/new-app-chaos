#!/usr/bin/env bash
# Apaga o cluster KIND do new-app-chaos (com tudo dentro, inclusive o banco do app no cluster).
set -euo pipefail
if kind get clusters 2>/dev/null | grep -qx new-app-chaos; then
  kind delete cluster --name new-app-chaos
else
  echo "o cluster new-app-chaos não existe"
fi
