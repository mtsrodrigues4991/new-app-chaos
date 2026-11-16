#!/bin/sh
# Passo 4 do sandbox: escreve o resultado no log do pod, que é por onde o backend o lê (o pod
# não tem acesso à API do cluster nem volume compartilhado com o app).
set -eu
b64() { if [ -s "$1" ]; then base64 -w0 "$1"; fi; }
tail -c 60000 /out/test.log > /out/test.tail 2>/dev/null || true
tail -c 4000 /out/agent.err > /out/agent.err.tail 2>/dev/null || true
echo "###NAC applied=$(cat /out/applied 2>/dev/null || echo 0)"
echo "###NAC test_exit=$(cat /out/test.exit 2>/dev/null || echo -1)"
echo "###NAC diff=$(b64 /out/final.diff)"
echo "###NAC agent=$(b64 /out/agent.json)"
echo "###NAC agent_err=$(b64 /out/agent.err.tail)"
echo "###NAC apply_err=$(b64 /out/apply.err)"
echo "###NAC test_log=$(b64 /out/test.tail)"
echo "###NAC done=1"
