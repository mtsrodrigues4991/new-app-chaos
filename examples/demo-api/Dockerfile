FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/demo-api .

FROM alpine:3.20
COPY --from=build /out/demo-api /usr/local/bin/demo-api
EXPOSE 8080
ENTRYPOINT ["demo-api"]
