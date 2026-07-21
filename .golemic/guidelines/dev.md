# Dev Guidelines

## Stack
Go 1.21, standard library only — no frameworks. Module `golemic`; non-public packages under `internal/`. The runner is deterministic and tool-driven; LLM judgement lives only in the role prompts.

## Build/Test
- Must stay green: `go build ./... && go test ./...`
- Keep clean: `gofmt`, `go vet`, `golangci-lint` (depguard enforces import layering).
- New logic test-driven; unit tests hermetic (inject dependencies, no real network/GitHub).

## Commits
Conventional Commits with slice number: `type(scope): summary (NNN)` — e.g. `fix(runner): … (018)`.

## Code Quality — Do's
- KISS, YAGNI, DRY (deduplicate knowledge, not every line).
- Small, clearly named packages; define small interfaces at the consumer; return concrete types.
- Inject dependencies explicitly; make zero values usable; prefer composition over abstraction.
- Wrap errors with `%w` and context; `context.Context` as the first parameter.
- Separate business logic from HTTP, DB, and infrastructure; one type / one function → one responsibility.

## Code Quality — Don'ts
- No abstractions/factories/managers/wrappers without a concrete need; no "God Interfaces".
- No `utils`/`common`/`helpers` packages; no unnecessarily deep package structures; no cyclic dependencies.
- No global mutable state; no hidden side effects.
- No panics for normal errors; do not ignore errors; do not destroy error chains with `%v`.
- Do not store `context.Context` in structs; no premature optimization; no clever one-liners at the cost of readability.
