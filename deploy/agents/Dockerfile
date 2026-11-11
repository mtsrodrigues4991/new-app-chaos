# Agentes Java para instrumentar alvos sem reconstruir a imagem deles (OBS=1): um
# initContainer copia os jars para um volume e o JAVA_TOOL_OPTIONS do alvo os carrega.
# - OpenTelemetry Java agent: métricas da JVM (heap, GC, threads), HTTP, pool do banco, traces.
# - Pyroscope Java agent: profiling contínuo (CPU e alocação).
FROM curlimages/curl:8.10.1 AS download
ARG OTEL_VERSION=2.31.0
ARG PYROSCOPE_VERSION=2.9.2
WORKDIR /tmp/agents
RUN curl -fsSL -o opentelemetry-javaagent.jar \
      "https://repo1.maven.org/maven2/io/opentelemetry/javaagent/opentelemetry-javaagent/${OTEL_VERSION}/opentelemetry-javaagent-${OTEL_VERSION}.jar" \
 && curl -fsSL -o pyroscope.jar \
      "https://repo1.maven.org/maven2/io/pyroscope/agent/${PYROSCOPE_VERSION}/agent-${PYROSCOPE_VERSION}.jar"

FROM busybox:1.36
COPY --from=download /tmp/agents /agents
