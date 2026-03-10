# ═══════════════════════════════════════════════════
#  Variables
# ═══════════════════════════════════════════════════
DATABASE_URL   ?= postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable
GOBIN          := $(shell pwd)/builds
ENVD_DIR       := envd
LDFLAGS        := -s -w

# ═══════════════════════════════════════════════════
#  Build
# ═══════════════════════════════════════════════════
.PHONY: build build-cp build-agent build-envd

build: build-cp build-agent build-envd

build-cp:
	go build -v -ldflags="$(LDFLAGS)" -o $(GOBIN)/wrenn-cp ./cmd/control-plane

build-agent:
	go build -v -ldflags="$(LDFLAGS)" -o $(GOBIN)/wrenn-agent ./cmd/host-agent

build-envd:
	cd $(ENVD_DIR) && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags="$(LDFLAGS)" -o $(GOBIN)/envd .
	@file $(GOBIN)/envd | grep -q "statically linked" || \
		(echo "ERROR: envd is not statically linked!" && exit 1)

# ═══════════════════════════════════════════════════
#  Development
# ═══════════════════════════════════════════════════
.PHONY: dev dev-cp dev-agent dev-envd dev-infra dev-down

## One command to start everything for local dev
dev: dev-infra migrate-up dev-cp

dev-infra:
	docker compose -f deploy/docker-compose.dev.yml up -d
	@echo "Waiting for PostgreSQL..."
	@until pg_isready -h localhost -p 5432 -q; do sleep 0.5; done
	@echo "Dev infrastructure ready."

dev-down:
	docker compose -f deploy/docker-compose.dev.yml down -v

dev-cp:
	@if command -v air > /dev/null; then air -c .air.cp.toml; \
	else go run ./cmd/control-plane; fi

dev-agent:
	sudo go run ./cmd/host-agent

dev-envd:
	cd $(ENVD_DIR) && go run . --debug --listen-tcp :3002


# ═══════════════════════════════════════════════════
#  Database (goose)
# ═══════════════════════════════════════════════════
.PHONY: migrate-up migrate-down migrate-status migrate-create migrate-reset

migrate-up:
	goose -dir db/migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir db/migrations postgres "$(DATABASE_URL)" down

migrate-status:
	goose -dir db/migrations postgres "$(DATABASE_URL)" status

migrate-create:
	goose -dir db/migrations create $(name) sql

migrate-reset:
	goose -dir db/migrations postgres "$(DATABASE_URL)" reset
	goose -dir db/migrations postgres "$(DATABASE_URL)" up

# ═══════════════════════════════════════════════════
#  Code Generation
# ═══════════════════════════════════════════════════
.PHONY: generate proto sqlc

generate: proto sqlc

proto:
	cd proto/envd && buf generate
	cd proto/hostagent && buf generate
	cd $(ENVD_DIR)/spec && buf generate

sqlc:
	sqlc generate

# ═══════════════════════════════════════════════════
#  Quality & Testing
# ═══════════════════════════════════════════════════
.PHONY: fmt lint vet test test-integration test-all tidy check

fmt:
	gofmt -w .
	cd $(ENVD_DIR) && gofmt -w .

lint:
	golangci-lint run ./...

vet:
	go vet ./...
	cd $(ENVD_DIR) && go vet ./...

test:
	go test -race -v ./internal/...

test-integration:
	go test -race -v -tags=integration ./tests/integration/...

test-all: test test-integration

tidy:
	go mod tidy
	cd $(ENVD_DIR) && go mod tidy

## Run all quality checks in CI order
check: fmt vet lint test

# ═══════════════════════════════════════════════════
#  Rootfs Images
# ═══════════════════════════════════════════════════
.PHONY: images image-minimal image-python image-node

images: build-envd image-minimal image-python image-node

image-minimal:
	sudo bash images/templates/minimal/build.sh

image-python:
	sudo bash images/templates/python311/build.sh

image-node:
	sudo bash images/templates/node20/build.sh

# ═══════════════════════════════════════════════════
#  Deployment
# ═══════════════════════════════════════════════════
.PHONY: setup-host install

setup-host:
	sudo bash scripts/setup-host.sh

install: build
	sudo cp $(GOBIN)/wrenn-cp /usr/local/bin/
	sudo cp $(GOBIN)/wrenn-agent /usr/local/bin/
	sudo cp deploy/systemd/*.service /etc/systemd/system/
	sudo systemctl daemon-reload

# ═══════════════════════════════════════════════════
#  Clean
# ═══════════════════════════════════════════════════
.PHONY: clean

clean:
	rm -rf builds/
	cd $(ENVD_DIR) && rm -f envd

# ═══════════════════════════════════════════════════
#  Help
# ═══════════════════════════════════════════════════
.DEFAULT_GOAL := help
.PHONY: help
help:
	@echo "Wrenn Sandbox"
	@echo ""
	@echo "  make dev            Full local dev (infra + migrate + seed + control plane)"
	@echo "  make dev-infra      Start PostgreSQL + Prometheus + Grafana"
	@echo "  make dev-down       Stop dev infra"
	@echo "  make dev-cp         Control plane (hot reload if air installed)"
	@echo "  make dev-agent      Host agent (sudo required)"
	@echo "  make dev-envd       envd in TCP debug mode"
	@echo ""
	@echo "  make build          Build all binaries → builds/"
	@echo "  make build-envd     Build envd static binary"
	@echo ""
	@echo "  make migrate-up     Apply migrations"
	@echo "  make migrate-create name=xxx  New migration"
	@echo "  make migrate-reset  Drop + re-apply all"
	@echo ""
	@echo "  make generate       Proto + sqlc codegen"
	@echo "  make check          fmt + vet + lint + test"
	@echo "  make test-all       Unit + integration tests"
	@echo ""
	@echo "  make images         Build all rootfs images"
	@echo "  make setup-host     One-time host setup"
	@echo "  make install        Install binaries + systemd units"
