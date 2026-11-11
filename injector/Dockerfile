# Container auxiliar que injeta falhas no alvo.
# Roda com --network container:<alvo> --cap-add NET_ADMIN para que o tc atue na rede do alvo.
FROM alpine:3.20
RUN apk add --no-cache iproute2 stress-ng procps
ENTRYPOINT ["sleep", "infinity"]
