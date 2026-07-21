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
The worktree is indexed into a code-intelligence graph. Prefer `golemic cbm search_graph`, `golemic cbm search_code`, `golemic cbm get_code_snippet`, `golemic cbm trace_call_path`, `golemic cbm query_graph`, `golemic cbm get_architecture`, and `golemic cbm get_graph_schema` over `grep`/`find`/broad `read` for structural exploration. Use `golemic cbm detect_changes` to understand the blast radius of the PR's modifications before reading files.

## Verdict
Exactly one `golemic submit-review --verdict approved|changes_requested`. When `changes_requested`, list concrete, actionable points.
