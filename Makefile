VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build agent-bins test lint dev-server dev-web images clean

all: web build

web:
	cd web && npm ci && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/srvmon-server ./cmd/server
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/srvmon-agent ./cmd/agent

# Static agent binaries for the systemd mode (also attached to v* releases).
agent-bins:
	for arch in amd64 arm64; do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o bin/agent/srvmon-agent-linux-$$arch ./cmd/agent || exit 1; \
	done

test:
	go test -race ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	cd web && npm run typecheck
	cd ai && npm run typecheck

# Local dev: API on :8080 with plain-HTTP cookies, Vite on :5173 proxies /api.
dev-server:
	SRVMON_DATA_DIR=./data SRVMON_SECURE_COOKIE=false SRVMON_AGENT_BIN_DIR=./bin/agent go run ./cmd/server serve

dev-web:
	cd web && npm run dev

images:
	docker build -f deploy/server.Dockerfile --build-arg VERSION=$(VERSION) -t srvmon-server:dev .
	docker build -f deploy/agent.Dockerfile --build-arg VERSION=$(VERSION) -t srvmon-agent:dev .
	docker build -f deploy/ai.Dockerfile --build-arg VERSION=$(VERSION) -t srvmon-ai:dev .

clean:
	rm -rf bin web/dist/assets web/dist/index.html web/dist/favicon.svg
