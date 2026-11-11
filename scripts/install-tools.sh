#!/usr/bin/env bash
# Instala kind, kubectl e helm em ~/.local/bin (Linux/WSL, amd64 ou arm64), sem sudo.
set -euo pipefail
KIND_VERSION="${KIND_VERSION:-v0.29.0}"
KUBECTL_VERSION="${KUBECTL_VERSION:-v1.33.1}"
HELM_VERSION="${HELM_VERSION:-v3.18.4}"
BIN="$HOME/.local/bin"
mkdir -p "$BIN"
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; *) echo "arquitetura não suportada: $(uname -m)"; exit 1 ;; esac

if ! command -v kind >/dev/null; then
  echo "instalando kind $KIND_VERSION"
  curl -fsSLo "$BIN/kind" "https://kind.sigs.k8s.io/dl/$KIND_VERSION/kind-linux-$ARCH" && chmod +x "$BIN/kind"
fi
if ! command -v kubectl >/dev/null; then
  echo "instalando kubectl $KUBECTL_VERSION"
  curl -fsSLo "$BIN/kubectl" "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/$ARCH/kubectl" && chmod +x "$BIN/kubectl"
fi
if ! command -v helm >/dev/null; then
  echo "instalando helm $HELM_VERSION"
  tmp="$(mktemp -d)"
  curl -fsSL "https://get.helm.sh/helm-$HELM_VERSION-linux-$ARCH.tar.gz" | tar -xz -C "$tmp"
  mv "$tmp/linux-$ARCH/helm" "$BIN/helm"; rm -rf "$tmp"
fi
case ":$PATH:" in *":$BIN:"*) ;; *) echo "acrescente ao ~/.bashrc: export PATH=\"\$HOME/.local/bin:\$PATH\"" ;; esac
kind version; kubectl version --client | head -1; helm version --short
