# Imagem do sandbox das correções automáticas: git para montar o código e o diff, e o
# Claude Code para o agente que edita. Não tem toolchain de build: o build e os testes rodam
# em outro container do mesmo pod, com a imagem configurada no cenário e sem nenhuma credencial.
FROM node:22-slim
# Versão fixa: as opções de linha de comando que o agent.sh usa foram conferidas nesta.
ARG CLAUDE_CODE_VERSION=2.1.326
RUN apt-get update && apt-get install -y --no-install-recommends git curl ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --no-fund --no-audit "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm cache clean --force
COPY checkout.sh agent.sh report.sh /usr/local/bin/
RUN chmod 755 /usr/local/bin/checkout.sh /usr/local/bin/agent.sh /usr/local/bin/report.sh
# O usuário "node" (uid 1000) já existe na imagem base.
USER 1000
ENV HOME=/work/home
WORKDIR /work
