# Development tasks for nodr. Run `make help` to list them.

GO      ?= go
BIN_DIR := bin
MODULE  := $(shell $(GO) list -m)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help build test test-race cover vet lint fmt tidy clean

help: ## List available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build bin/nodr
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/nodr ./cmd/nodr

test: ## Run the unit tests
	$(GO) test ./...

test-race: ## Run the unit tests with the race detector
	$(GO) test -race ./...

cover: ## Run the unit tests and print total coverage
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

vet: ## Run go vet
	$(GO) vet ./...

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format the code
	golangci-lint fmt

tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

clean: ## Remove build and coverage output
	rm -rf $(BIN_DIR) coverage.out
