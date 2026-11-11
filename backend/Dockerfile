FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

FROM alpine:3.20
COPY --from=build /out/server /usr/local/bin/server
EXPOSE 8080
ENTRYPOINT ["server"]
