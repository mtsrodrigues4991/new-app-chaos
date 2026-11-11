FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server
# Binário estático copiado para dentro do alvo nas falhas de CPU e memória.
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/stressor ./cmd/stressor

FROM alpine:3.20
# iproute2 (tc): esta mesma imagem é usada como auxiliar nas falhas de rede.
RUN apk add --no-cache iproute2
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/stressor /usr/local/lib/new-app-chaos/stressor
EXPOSE 8080
ENTRYPOINT ["server"]
