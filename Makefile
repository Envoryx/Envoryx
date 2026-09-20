VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO      ?= go
NPM     ?= npm
IMAGE   ?= ghcr.io/envoryx/envoryx

.PHONY: all build web backend run test test-go test-web test-integration test-e2e lint docker clean

all: build

## Build frontend + backend into ./bin/envoryx
build: web backend

web:
	cd web && $(NPM) ci --no-audit --no-fund && $(NPM) run build
	@touch web/dist/.gitkeep

backend:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/envoryx ./cmd/envoryx

## Run the backend locally (frontend via `cd web && npm run dev`)
run:
	ENVORYX_DEV=1 ENVORYX_CONFIG_DIR=$(CURDIR)/.local/config ENVORYX_PROJECTS_DIR=$(CURDIR)/.local/projects \
	ENVORYX_PORT_RANGE_START=20000 ENVORYX_PORT_RANGE_END=20099 $(GO) run ./cmd/envoryx serve

test: test-go test-web

test-go:
	$(GO) vet ./...
	$(GO) test -race ./...

test-web:
	cd web && $(NPM) run lint && $(NPM) test

## Integration tests against a real Docker engine
test-integration:
	$(GO) test -tags integration -count=1 ./internal/docker/ ./internal/s3/

## Browser tests against ./bin/envoryx and a real Docker engine (needs `make build` and
## `cd web && npx playwright install chromium` once)
test-e2e:
	cd web && $(NPM) run test:e2e

lint:
	gofmt -l . && $(GO) vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

clean:
	rm -rf bin web/dist/* && touch web/dist/.gitkeep
