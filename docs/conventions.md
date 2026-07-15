# Golemic — Project Conventions

## Language

**English is the project language for all produced artifacts.** This includes:

- Source code (identifiers, comments, error messages, log strings)
- Tests (names, assertions, table headers)
- Documentation (`docs/`, `README`, `CLAUDE.md`, agent guidelines)
- Commit messages and PR titles/bodies
- Task briefings, review findings, backlog items, prompts, and templates

The human maintainer may converse with agents in German. That is a communication
channel, not a project artifact. Anything that lands on disk or in git history
is English.

Rationale: the tool is agent-agnostic and may be operated by contributors,
LLMs, or CI in any locale. A single language for artifacts keeps the codebase
searchable, reviewable, and portable.

Exception: user-facing quotes from the maintainer inside issues or discussions
may remain in the original language if quoted verbatim.
