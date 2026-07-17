LINT_BASE_REF ?= origin/main

.PHONY: build
build: ## Build the golemic binary to ./golemic
	go build -o golemic ./cmd/golemic

.PHONY: lint
lint: ## Run golangci-lint: changed-lines (complexity/standard) + repo-wide architecture rules
	golangci-lint run --new-from-rev=$(LINT_BASE_REF)
	golangci-lint run -c .golangci-arch.yml

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'
