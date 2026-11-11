# Kubernetes local (KIND). O modo Docker continua com: docker compose up --build
.PHONY: kind-up kind-app kind-down kind-tools test

## Cria o cluster KIND e instala metrics-server, Chaos Mesh, new-app-chaos e os alvos.
kind-up:
	./scripts/kind-up.sh

## Reconstrói e reinstala só o new-app-chaos (backend e frontend) no cluster.
kind-app:
	ONLY_APP=1 ./scripts/kind-up.sh

## Apaga o cluster.
kind-down:
	./scripts/kind-down.sh

## Instala kind, kubectl e helm em ~/.local/bin.
kind-tools:
	./scripts/install-tools.sh

## Testes do backend e checagem de tipos do frontend.
test:
	cd backend && go vet ./... && go test ./...
	cd frontend && npx tsc --noEmit -p .
