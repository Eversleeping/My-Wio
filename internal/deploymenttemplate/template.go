package deploymenttemplate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ModeAuto = "auto"

	NodeViteStatic       = "node-vite-static"
	NodeViteWebsocket    = "node-vite-websocket"
	TemplateVersion      = "1"
	GeneratedDockerfile  = "Dockerfile.generated"
	GeneratedNginxConfig = "nginx.generated.conf"
)

type Template struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	Label         string `json:"label"`
	Description   string `json:"description"`
	ContainerPort int    `json:"container_port"`
	SupportsPull  bool   `json:"supports_pull"`
}

type GenerateOptions struct {
	Root        string
	ComposeFile string
	TemplateID  string
	BuildMode   string
	PublicPort  int
}

type Result struct {
	Template    Template
	ComposePath string
	Generated   bool
	Files       []string
}

type packageManifest struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

var templates = []Template{
	{
		ID:            NodeViteWebsocket,
		Version:       TemplateVersion,
		Label:         "Node.js Vite + WebSocket",
		Description:   "Builds a Vite frontend and runs a Node.js HTTP/WebSocket service behind Nginx.",
		ContainerPort: 80,
		SupportsPull:  false,
	},
	{
		ID:            NodeViteStatic,
		Version:       TemplateVersion,
		Label:         "Node.js Vite static site",
		Description:   "Builds a Vite frontend and serves the static output with Nginx.",
		ContainerPort: 80,
		SupportsPull:  false,
	},
}

func List() []Template {
	result := make([]Template, len(templates))
	copy(result, templates)
	return result
}

func Detect(root string) (Template, error) {
	root = filepath.Clean(root)
	if root == "." || !filepath.IsAbs(root) {
		return Template{}, errors.New("template root must be an absolute directory")
	}
	manifestPath := filepath.Join(root, "package.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return Template{}, fmt.Errorf("package manifest: %w", err)
	}
	var manifest packageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Template{}, fmt.Errorf("package manifest: %w", err)
	}
	hasVite := hasPackage(manifest, "vite") || hasFileWithPrefix(root, "vite.config.")
	hasPnpmLock := fileExists(root, "pnpm-lock.yaml")
	if !hasPnpmLock {
		return Template{}, errors.New("supported Node.js templates require pnpm-lock.yaml")
	}
	if hasVite && hasServerEntry(root) && strings.TrimSpace(manifest.Scripts["server"]) != "" {
		return templateByID(NodeViteWebsocket)
	}
	if hasVite && strings.TrimSpace(manifest.Scripts["build"]) != "" {
		return templateByID(NodeViteStatic)
	}
	return Template{}, errors.New("no supported deployment template matched this project")
}

func Generate(options GenerateOptions) (Result, error) {
	root := filepath.Clean(options.Root)
	if root == "." || !filepath.IsAbs(root) {
		return Result{}, errors.New("template root must be an absolute directory")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		if err != nil {
			return Result{}, fmt.Errorf("template root: %w", err)
		}
		return Result{}, errors.New("template root is not a directory")
	}
	composeFile, err := safeRelativePath(options.ComposeFile)
	if err != nil {
		return Result{}, fmt.Errorf("generated Compose file: %w", err)
	}
	composePath := filepath.Join(root, composeFile)
	if _, err := os.Lstat(composePath); err == nil {
		return Result{}, errors.New("generated Compose file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect generated Compose file: %w", err)
	}
	template := Template{}
	if strings.TrimSpace(options.TemplateID) == "" || options.TemplateID == ModeAuto {
		template, err = Detect(root)
	} else {
		template, err = templateByID(options.TemplateID)
	}
	if err != nil {
		return Result{}, err
	}
	if options.BuildMode == "pull" && !template.SupportsPull {
		return Result{}, fmt.Errorf("template %s requires build mode", template.ID)
	}
	composeDir := filepath.ToSlash(filepath.Dir(composeFile))
	contextPath, err := filepath.Rel(composeDir, ".")
	if err != nil {
		return Result{}, fmt.Errorf("resolve generated build context: %w", err)
	}
	contextPath = filepath.ToSlash(contextPath)
	if contextPath == "" {
		contextPath = "."
	}

	files := make([]string, 0, 4)
	var dockerfile, nginx, compose string
	switch template.ID {
	case NodeViteWebsocket:
		dockerfile = nodeViteWebsocketDockerfile
		nginx = nodeViteWebsocketNginx
		compose = nodeViteWebsocketCompose(options.PublicPort > 0, contextPath)
	case NodeViteStatic:
		dockerfile = nodeViteStaticDockerfile
		nginx = nodeViteStaticNginx
		compose = nodeViteStaticCompose(options.PublicPort > 0, contextPath)
	default:
		return Result{}, fmt.Errorf("template %s is not implemented", template.ID)
	}
	if err := writeGenerated(root, GeneratedDockerfile, []byte(dockerfile)); err != nil {
		return Result{}, err
	}
	files = append(files, GeneratedDockerfile)
	if err := writeGenerated(root, GeneratedNginxConfig, []byte(nginx)); err != nil {
		return Result{}, err
	}
	files = append(files, GeneratedNginxConfig)
	if err := writeGenerated(root, composeFile, []byte(compose)); err != nil {
		return Result{}, err
	}
	files = append(files, composeFile)
	if _, err := os.Lstat(filepath.Join(root, ".dockerignore")); errors.Is(err, os.ErrNotExist) {
		if err := writeGenerated(root, ".dockerignore", []byte(generatedDockerignore)); err != nil {
			return Result{}, err
		}
		files = append(files, ".dockerignore")
	} else if err != nil {
		return Result{}, fmt.Errorf("inspect .dockerignore: %w", err)
	}
	return Result{Template: template, ComposePath: composePath, Generated: true, Files: files}, nil
}

func templateByID(id string) (Template, error) {
	for _, template := range templates {
		if template.ID == id {
			return template, nil
		}
	}
	return Template{}, fmt.Errorf("unknown deployment template %q", id)
}

func hasPackage(manifest packageManifest, name string) bool {
	_, dependency := manifest.Dependencies[name]
	_, devDependency := manifest.DevDependencies[name]
	return dependency || devDependency
}

func hasServerEntry(root string) bool {
	for _, name := range []string{"server/index.ts", "server/index.js", "server/index.mjs"} {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func hasFileWithPrefix(root, prefix string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			return true
		}
	}
	return false
}

func fileExists(root, relative string) bool {
	info, err := os.Stat(filepath.Join(root, relative))
	return err == nil && !info.IsDir()
}

func safeRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "compose.yaml"
	}
	if filepath.IsAbs(value) {
		return "", errors.New("path must be relative")
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the release directory")
	}
	return clean, nil
}

func writeGenerated(root, relative string, data []byte) error {
	path, err := safeRelativePath(relative)
	if err != nil {
		return err
	}
	file := filepath.Join(root, path)
	if info, err := os.Lstat(file); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite symlink %s", relative)
		}
		return fmt.Errorf("generated file already exists: %s", relative)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect generated file %s: %w", relative, err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return fmt.Errorf("create generated file directory: %w", err)
	}
	if err := os.WriteFile(file, data, 0o640); err != nil {
		return fmt.Errorf("write generated file %s: %w", relative, err)
	}
	return nil
}

func nodeViteWebsocketCompose(publishPort bool, contextPath string) string {
	ports := ""
	if publishPort {
		ports = "    ports:\n      - \"${APP_PORT:?set APP_PORT}:80\"\n"
	}
	return fmt.Sprintf(`services:
  api:
    build:
      context: %s
      dockerfile: Dockerfile.generated
      target: server
    environment:
      NODE_ENV: production
      HOST: 0.0.0.0
      PORT: 8787
      LB_DATA: /var/lib/app/leaderboard.json
    volumes:
      - app_data:/var/lib/app
    expose:
      - "8787"
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O - http://127.0.0.1:8787/api/top >/dev/null || exit 1"]
      interval: 10s
      timeout: 3s
      retries: 12

  web:
    build:
      context: %s
      dockerfile: Dockerfile.generated
      target: web
    depends_on:
      api:
        condition: service_healthy
    expose:
      - "80"
%s    healthcheck:
      test: ["CMD-SHELL", "wget -q -O - http://127.0.0.1/ >/dev/null || exit 1"]
      interval: 10s
      timeout: 3s
      retries: 12

volumes:
  app_data:
`, contextPath, contextPath, ports)
}

func nodeViteStaticCompose(publishPort bool, contextPath string) string {
	ports := ""
	if publishPort {
		ports = "    ports:\n      - \"${APP_PORT:?set APP_PORT}:80\"\n"
	}
	return fmt.Sprintf(`services:
  web:
    build:
      context: %s
      dockerfile: Dockerfile.generated
      target: web
    expose:
      - "80"
%s    healthcheck:
      test: ["CMD-SHELL", "wget -q -O - http://127.0.0.1/ >/dev/null || exit 1"]
      interval: 10s
      timeout: 3s
      retries: 12
`, contextPath, ports)
}

const nodeViteWebsocketDockerfile = `FROM node:22-alpine AS dependencies
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@9.15.5 --activate
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .

FROM dependencies AS build
RUN pnpm build

FROM dependencies AS server
ENV NODE_ENV=production
ENV HOST=0.0.0.0
ENV PORT=8787
ENV LB_DATA=/var/lib/app/leaderboard.json
RUN mkdir -p /var/lib/app
EXPOSE 8787
CMD ["pnpm", "run", "server"]

FROM nginx:1.27-alpine AS web
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.generated.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
`

const nodeViteStaticDockerfile = `FROM node:22-alpine AS dependencies
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@9.15.5 --activate
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .

FROM dependencies AS build
RUN pnpm build

FROM nginx:1.27-alpine AS web
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.generated.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
`

const nodeViteWebsocketNginx = `server {
    listen 80;
    server_name _;
    root /usr/share/nginx/html;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }

    location /api/ {
        proxy_pass http://api:8787;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location = /ws {
        proxy_pass http://api:8787/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}
`

const nodeViteStaticNginx = `server {
    listen 80;
    server_name _;
    root /usr/share/nginx/html;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }
}
`

const generatedDockerignore = `.git
node_modules
dist
.env
.env.*
server/data
coverage
`
