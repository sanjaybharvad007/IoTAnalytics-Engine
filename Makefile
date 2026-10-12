SHELL := /bin/bash
.DEFAULT_GOAL := help

BIN       := bin
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
GOFLAGS   := -trimpath
PKGS      := ./...
COVER_OUT := coverage.out

## ---- build ---------------------------------------------------------------

.PHONY: build
build: ## Build server and simulator binaries into ./bin
	@mkdir -p $(BIN)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/server ./cmd/server
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)/simulator ./cmd/simulator

.PHONY: run
run: ## Run the server with an in-process 8-vehicle simulator
	go run ./cmd/server -simulate 8

.PHONY: sim
sim: ## Run the external device simulator against localhost:8080
	go run ./cmd/simulator -url http://localhost:8080 -vehicles 6

## ---- quality -------------------------------------------------------------

.PHONY: fmt
fmt: ## gofmt all Go sources
	gofmt -s -w $(shell find . -name '*.go' -not -path './client-ts/*')

.PHONY: vet
vet: ## go vet
	go vet $(PKGS)

.PHONY: lint
lint: vet ## golangci-lint (if installed) + gofmt check
	@test -z "$$(gofmt -l . | grep -v client-ts)" || { echo 'gofmt: files need formatting:'; gofmt -l .; exit 1; }
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed; skipped"

.PHONY: test
test: ## Go unit tests
	go test -count=1 $(PKGS)

.PHONY: race
race: ## Go tests under the race detector
	go test -race -count=1 $(PKGS)

.PHONY: cover
cover: ## Coverage report (coverage.out + summary)
	go test -race -count=1 -coverprofile=$(COVER_OUT) -covermode=atomic $(PKGS)
	@go tool cover -func=$(COVER_OUT) | tail -1

.PHONY: cover-html
cover-html: cover ## Open an HTML coverage report
	go tool cover -html=$(COVER_OUT)

.PHONY: bench
bench: ## Micro-benchmarks for the hot paths
	go test -run '^$$' -bench . -benchmem $(PKGS)

.PHONY: ts
ts: ## TypeScript client: install, type-check and test
	cd client-ts && npm ci && npm test

.PHONY: check
check: lint race ts ## Everything CI runs

## ---- containers ----------------------------------------------------------

IMAGE ?= iotanalytics/engine

.PHONY: docker
docker: ## Build the server container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: up
up: ## Start engine + Prometheus + Grafana with docker compose
	docker compose up --build -d
	@echo "dashboard  http://localhost:8080"
	@echo "grafana    http://localhost:3000"
	@echo "prometheus http://localhost:9090"

.PHONY: down
down: ## Stop the compose stack (keeps volumes)
	docker compose down

.PHONY: logs
logs: ## Tail engine logs
	docker compose logs -f engine

## ---- misc ----------------------------------------------------------------

.PHONY: clean
clean: ## Remove build artefacts and local data
	rm -rf $(BIN) data $(COVER_OUT) client-ts/dist

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage: make \033[36m<target>\033[0m\n\n"} \
	     /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 } \
	     /^## ----/ { printf "\n" }' $(MAKEFILE_LIST)
