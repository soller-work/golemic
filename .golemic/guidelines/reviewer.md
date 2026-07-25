# Reviewer Guidelines

## Stack
Go 1.21, standard library only — no frameworks. Module `golemic`; non-public packages under `internal/`.

## Verification
- `go build ./... && go test ./...` must be green — otherwise `changes_requested`.
- `gofmt`, `go vet`, `golangci-lint` (depguard layering) must be clean.
- Diff must satisfy the issue — no more (no scope creep), no less.
- Commits: Conventional Commits with slice number `type(scope): summary (NNN)`.

## Check against — Do's
- KISS/YAGNI/DRY respected; small, clearly named packages; one type / one function → one responsibility (SRP per struct and package).
- Small interfaces at the consumer; concrete return types; dependencies explicitly injected; zero values usable.
- Errors wrapped with `%w`; `context.Context` as the first parameter; business logic separated from HTTP/DB/infra.

## Check against — Don'ts
- Abstractions/factories/managers/wrappers without need; "God Interfaces"; `utils`/`common`/`helpers`; deep package structures; cyclic dependencies.
- Global mutable state; hidden side effects; panics for normal errors; ignored errors; `%v` in error chains.
- `context.Context` stored in structs; premature optimization; clever one-liners at the cost of readability.

## Exploring the Codebase
The worktree is indexed into a code-intelligence graph. Prefer `gm_code_search_graph`, `gm_code_search`, `gm_code_get_snippet`, `gm_code_trace_call_path`, `gm_code_query_graph`, `gm_code_get_architecture`, and `gm_code_get_graph_schema` over `grep`/`find`/broad `read` for structural exploration. Use `gm_code_detect_changes` to understand the blast radius of the PR's modifications before reading files.

## Verdict
Exactly one `gm_review_submit` with `verdict="approved"|"changes_requested"`. When `changes_requested`, list concrete, actionable points.
