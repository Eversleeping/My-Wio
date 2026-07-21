FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm config set fetch-retries 5 \
  && npm config set fetch-retry-mintimeout 10000 \
  && npm config set fetch-retry-maxtimeout 120000 \
  && npm config set fetch-timeout 300000 \
  && npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.22-alpine AS go
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wio-controlplane ./cmd/controlplane
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /wio-agent-linux-amd64 ./cmd/agent
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /wio-agent-linux-arm64 ./cmd/agent

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && addgroup -S wio && adduser -S -G wio wio
USER wio
COPY --from=go /wio-controlplane /usr/local/bin/wio-controlplane
COPY --from=go /wio-agent-linux-amd64 /usr/local/share/wio/wio-agent-linux-amd64
COPY --from=go /wio-agent-linux-arm64 /usr/local/share/wio/wio-agent-linux-arm64
COPY --from=go /src/deploy/agent.service /usr/local/share/wio/wio-agent.service
EXPOSE 8080
ENTRYPOINT ["wio-controlplane"]
