LINT_BASE_REF ?= origin/main
GOLANGCI_LINT_VERSION ?= v1.62.2
GOBIN := $(shell go env GOPATH)/bin

.PHONY: build
build: ## Build the golemic binary to ./golemic
	go build -o golemic ./cmd/golemic

$(GOBIN)/golangci-lint:
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOBIN) $(GOLANGCI_LINT_VERSION)

.PHONY: test-e2e
test-e2e: build ## Run e2e suite (build-tagged; needs golemic_e2e sandbox + GOLEMIC_E2E_PATH)
	GOLEMIC_BINARY=$(PWD)/golemic go test -tags e2e ./test/e2e/...

COMPLEXITY_LINTERS := cyclop|gocognit|funlen|nestif|maintidx|interfacebloat

.PHONY: lint-no-prod-nolint
lint-no-prod-nolint: ## Ban complexity //nolint directives in production Go files (cmd/**/*.go, internal/**/*.go, excl. _test.go)
	@violations=$$(git ls-files 'cmd/**/*.go' 'internal/**/*.go' \
		| grep -v '_test\.go$$' \
		| xargs grep -EHn '//nolint:[^/]*\b($(COMPLEXITY_LINTERS))\b' 2>/dev/null || true); \
	if [ -n "$$violations" ]; then \
		echo "$$violations" >&2; \
		echo "ERROR: complexity nolint directives are forbidden in production code; split the function instead" >&2; \
		exit 1; \
	fi

.PHONY: lint
lint: $(GOBIN)/golangci-lint ## Run golangci-lint: changed-lines (complexity/standard) + repo-wide architecture rules; bans complexity nolint directives in production code (cyclop, gocognit, funlen, nestif, maintidx, interfacebloat)
	$(GOBIN)/golangci-lint run --new-from-rev=$(LINT_BASE_REF)
	$(GOBIN)/golangci-lint run -c .golangci-arch.yml
	$(MAKE) lint-no-prod-nolint

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'
