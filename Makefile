.PHONY: dev web build agent-linux test test-postgres

dev:
	go run ./cmd/controlplane

web:
	cd web && npm run dev

build:
	mkdir -p bin
	go build -o bin/wio-controlplane ./cmd/controlplane
	go build -o bin/wio-agent ./cmd/agent
	npm --prefix web run build

agent-linux:
	mkdir -p bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/wio-agent-linux-amd64 ./cmd/agent

test: test-postgres
	npm --prefix web run build
	go test ./...
	npm --prefix web test
	npm --prefix web run typecheck

test-postgres:
	go test -tags=postgresintegration ./internal/store -run TestPostgresMigrationAndStorage -count=1
