.PHONY: dev web build agent-linux test test-postgres fmt-check verify

dev:
	go run ./cmd/controlplane

web:
	cd web && npm run dev

build:
	mkdir -p bin
	npm --prefix web run build
	go build -o bin/wio-controlplane ./cmd/controlplane
	go build -o bin/wio-agent ./cmd/agent

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

fmt-check:
	@files="$$(gofmt -l cmd internal web/embed.go)" || exit $$?; \
		test -z "$$files" || { printf '%s\n' "$$files"; exit 1; }

verify: fmt-check
	npm --prefix web ci
	npm --prefix web run build
	npm --prefix web test
	npm --prefix web run typecheck
	go vet ./...
	go test ./...
	go build ./cmd/controlplane ./cmd/agent
