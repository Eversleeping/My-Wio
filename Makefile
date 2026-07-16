.PHONY: dev web build agent-linux test

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

test:
	go test ./...
	npm --prefix web run typecheck
