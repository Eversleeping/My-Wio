.PHONY: dev web build agent-linux agent-all test test-postgres fmt-check staticcheck version-check verify

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
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o bin/wio-agent-linux-arm64 ./cmd/agent

agent-all: agent-linux
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/wio-agent-darwin-amd64 ./cmd/agent
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o bin/wio-agent-darwin-arm64 ./cmd/agent
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/wio-agent-windows-amd64.exe ./cmd/agent

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

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@2024.1.1 ./...

version-check:
	scripts/check-agent-version.sh

verify: fmt-check
	npm --prefix web ci
	npm --prefix web run build
	$(MAKE) version-check
	scripts/test-check-agent-version.sh
	npm --prefix web test
	npm --prefix web run typecheck
	$(MAKE) staticcheck
	go vet ./...
	go test ./...
	go build ./cmd/controlplane ./cmd/agent
