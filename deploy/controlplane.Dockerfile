FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.22-alpine AS go
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wio-controlplane ./cmd/controlplane

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && addgroup -S wio && adduser -S -G wio wio
USER wio
COPY --from=go /wio-controlplane /usr/local/bin/wio-controlplane
EXPOSE 8080
ENTRYPOINT ["wio-controlplane"]

