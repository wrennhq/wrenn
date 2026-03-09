BUILD     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS   := -s -w -X=main.commitSHA=$(BUILD)
BUILDS    := ../builds

# ═══════════════════════════════════════════════════
#  Build
# ═══════════════════════════════════════════════════
.PHONY: build build-debug

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(BUILDS)/envd .
	@file $(BUILDS)/envd | grep -q "statically linked" || \
		(echo "ERROR: envd is not statically linked!" && exit 1)

build-debug:
	CGO_ENABLED=1 go build -race -gcflags=all="-N -l" -ldflags="-X=main.commitSHA=$(BUILD)" -o $(BUILDS)/debug/envd .

# ═══════════════════════════════════════════════════
#  Run (debug mode, not inside a VM)
# ═══════════════════════════════════════════════════
.PHONY: run-debug

run-debug: build-debug
	$(BUILDS)/debug/envd -isnotfc -port 49983

# ═══════════════════════════════════════════════════
#  Code Generation
# ═══════════════════════════════════════════════════
.PHONY: generate proto openapi

generate: proto openapi

proto:
	cd spec && buf generate --template buf.gen.yaml

openapi:
	go generate ./internal/api/...

# ═══════════════════════════════════════════════════
#  Quality
# ═══════════════════════════════════════════════════
.PHONY: fmt vet test tidy

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test -race -v ./...

tidy:
	go mod tidy

# ═══════════════════════════════════════════════════
#  Clean
# ═══════════════════════════════════════════════════
.PHONY: clean

clean:
	rm -f $(BUILDS)/envd $(BUILDS)/debug/envd
