FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.22-alpine AS go
WORKDIR /src
ENV GOMAXPROCS=1 GOFLAGS=-p=1
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wio-controlplane ./cmd/controlplane
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /wio-agent-linux-amd64 ./cmd/agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /wio-agent-linux-arm64 ./cmd/agent

FROM node:22-alpine
ARG CODEX_VERSION=0.144.4
ARG GEMINI_VERSION=0.65.0
ARG CLAUDE_VERSION=2.1.318
RUN apk add --no-cache bash ca-certificates docker-cli docker-cli-compose git tzdata \
	&& npm install --global --omit=dev @openai/codex@${CODEX_VERSION} @google/gemini-cli@${GEMINI_VERSION} @anthropic-ai/claude-code@${CLAUDE_VERSION} \
    && mkdir -p /var/lib/wio-agent/projects \
    && chmod 700 /var/lib/wio-agent
ENV HOME=/var/lib/wio-agent
# The built-in Agent runs as root inside this container. Claude Code refuses
# bypassPermissions for root unless the environment is declared a sandbox, so
# without this the mode fails here while it works on managed servers.
ENV IS_SANDBOX=1
COPY --from=go /wio-controlplane /usr/local/bin/wio-controlplane
COPY --from=go /wio-agent-linux-amd64 /usr/local/share/wio/wio-agent-linux-amd64
COPY --from=go /wio-agent-linux-arm64 /usr/local/share/wio/wio-agent-linux-arm64
COPY --from=go /src/deploy/agent.service /usr/local/share/wio/wio-agent.service
COPY --from=go /src/deploy/prerequisite.service /usr/local/share/wio/wio-prerequisite.service
EXPOSE 8080
ENTRYPOINT ["wio-controlplane"]
