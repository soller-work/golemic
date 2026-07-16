LINT_BASE_REF ?= origin/main

.PHONY: lint
lint: ## Run golangci-lint against lines changed since LINT_BASE_REF
	golangci-lint run --new-from-rev=$(LINT_BASE_REF)

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'
