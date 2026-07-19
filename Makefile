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

.PHONY: lint
lint: $(GOBIN)/golangci-lint ## Run golangci-lint: changed-lines (complexity/standard) + repo-wide architecture rules
	$(GOBIN)/golangci-lint run --new-from-rev=$(LINT_BASE_REF)
	$(GOBIN)/golangci-lint run -c .golangci-arch.yml

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'
