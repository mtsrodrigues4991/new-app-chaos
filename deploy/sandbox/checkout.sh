#!/bin/sh
# Passo 1 do sandbox: baixa o código do commit testado, registra a linha de base e tenta aplicar
# o patch como veio. Não tem modelo nem a chave da Anthropic.
#
# Entrada: REPO (dono/repo), COMMIT (sha), PATCH_DIR (subpasta a que o patch é relativo; vazio =
# raiz), /in/patch.diff e, para repositório privado, /in/GITHUB_TOKEN (só leitura).
# Saída: /work/repo (um repositório git com um commit de base) e /out/applied (1 ou 0).
set -eu
mkdir -p "$HOME" /work/repo /out
cd /work/repo

auth=""
[ -s /in/GITHUB_TOKEN ] && auth="Authorization: Bearer $(cat /in/GITHUB_TOKEN)"
echo "baixando $REPO@$COMMIT"
curl -fsSL --retry 3 ${auth:+-H "$auth"} -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/$REPO/tarball/$COMMIT" | tar -xz --strip-components=1

git init -q
git config user.email "sandbox@new-app-chaos.local"
git config user.name "new-app-chaos"
git add -A
git commit -q -m "base $COMMIT"

if git apply --whitespace=nowarn --recount ${PATCH_DIR:+--directory="$PATCH_DIR"} /in/patch.diff 2> /out/apply.err; then
  echo 1 > /out/applied
  echo "o patch aplicou como veio"
else
  echo 0 > /out/applied
  echo "o patch não aplicou como veio:"; cat /out/apply.err
fi
