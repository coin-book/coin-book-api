GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT := ./bin/golangci-lint
OPENAPI_FILE := openapi.yaml
APP_NAME := coin-book-api

GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo nogit)
BUILD_VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo $(GIT_SHA))

.DEFAULT_GOAL := help
.PHONY: help build test test-integration lint fmt openapi openapi-check docker

help: ## Show this help
	@grep -hE '^[a-z][a-zA-Z0-9_-]*:.*##' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary into ./tmp
	@mkdir -p tmp
	go build -trimpath -ldflags="-X main.buildVersion=$(BUILD_VERSION) -X main.buildCommit=$(GIT_SHA)" \
		-o tmp/$(APP_NAME) ./cmd/$(APP_NAME)

test: ## Unit and golden tests
	go test ./...

test-integration: ## Acceptance and repository tests against Postgres in testcontainers (needs Docker)
	go test -count=1 -tags integration ./...

lint: $(GOLANGCI_LINT) ## golangci-lint, including the integration-tagged tests
	$(GOLANGCI_LINT) run --build-tags integration ./...

fmt: $(GOLANGCI_LINT) ## Format and tidy
	$(GOLANGCI_LINT) fmt ./...
	go mod tidy

openapi: ## Regenerate the committed OpenAPI contract from the handlers
	go run ./cmd/openapi > $(OPENAPI_FILE)

openapi-check: ## Fail when openapi.yaml is out of date
	@go run ./cmd/openapi | diff -u $(OPENAPI_FILE) - || (echo "openapi.yaml is stale: run make openapi" && exit 1)

docker: ## Build the container image
	docker build --build-arg BUILD_VERSION=$(BUILD_VERSION) --build-arg BUILD_COMMIT=$(GIT_SHA) -t $(APP_NAME):$(BUILD_VERSION) .

$(GOLANGCI_LINT):
	@mkdir -p bin
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b ./bin $(GOLANGCI_LINT_VERSION)
