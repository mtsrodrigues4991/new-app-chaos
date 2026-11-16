#!/bin/sh
# Passo 2 do sandbox: o agente edita o código. Só roda quando é preciso (o patch não aplicou
# como veio, ou o build da rodada anterior falhou). É o único passo com a chave da Anthropic e
# o único com modelo. Por isso:
#   --tools        só existem as ferramentas de arquivo: ele não executa comandos nem acessa a rede;
#   --restricted   as ferramentas de arquivo ficam confinadas à pasta do repositório;
#   --bare         hooks, plugins e o CLAUDE.md do repositório (conteúdo não confiável) são ignorados;
#   --max-budget-usd  teto de gasto da rodada.
#
# Entrada: RUN_AGENT (1 = roda), MODEL, MAX_BUDGET_USD, /in/task.md, /in/ANTHROPIC_API_KEY.
# Saída: /out/agent.json (saída do Claude Code) e /out/final.diff (tudo que mudou sobre a base).
set -eu
cd /work/repo
if [ "${RUN_AGENT:-0}" = 1 ]; then
  ANTHROPIC_API_KEY="$(cat /in/ANTHROPIC_API_KEY)"
  export ANTHROPIC_API_KEY
  export DISABLE_AUTOUPDATER=1 DISABLE_TELEMETRY=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
  claude -p "$(cat /in/task.md)" \
    --output-format json \
    --model "$MODEL" \
    --bare --restricted --strict-mcp-config --no-session-persistence \
    --tools "Read,Edit,Write,Glob,Grep" \
    --permission-mode acceptEdits \
    --max-budget-usd "${MAX_BUDGET_USD:-1}" \
    > /out/agent.json 2> /out/agent.err || echo "o agente terminou com erro: $(tail -c 300 /out/agent.err)"
  unset ANTHROPIC_API_KEY
else
  echo "agente dispensado: o patch aplicou como veio"
fi
# O diff é tirado agora, antes do build, para não levar junto o que o build gerar.
git add -A
git diff --cached HEAD > /out/final.diff
echo "diff final: $(wc -l < /out/final.diff) linhas"
