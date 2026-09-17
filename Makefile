VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO      ?= go
NPM     ?= npm
IMAGE   ?= ghcr.io/seramos/staqio

.PHONY: all build web backend run test test-go test-web test-integration lint docker clean

all: build

## Build frontend + backend into ./bin/staqio
build: web backend

web:
	cd web && $(NPM) ci --no-audit --no-fund && $(NPM) run build
	@touch web/dist/.gitkeep

backend:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/staqio ./cmd/staqio

## Run the backend locally (frontend via `cd web && npm run dev`)
run:
	STAQIO_DEV=1 STAQIO_CONFIG_DIR=$(CURDIR)/.local/config STAQIO_PROJECTS_DIR=$(CURDIR)/.local/projects \
	STAQIO_PORT_RANGE_START=20000 STAQIO_PORT_RANGE_END=20099 $(GO) run ./cmd/staqio serve

test: test-go test-web

test-go:
	$(GO) vet ./...
	$(GO) test -race ./...

test-web:
	cd web && $(NPM) run lint && $(NPM) test

## Integration tests against a real Docker engine
test-integration:
	$(GO) test -tags integration -count=1 ./internal/docker/

lint:
	gofmt -l . && $(GO) vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

clean:
	rm -rf bin web/dist/* && touch web/dist/.gitkeep
