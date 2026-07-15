# Slice type rules

Choose exactly one primary `slice_type`. The type defines the minimum contract required for autonomous implementation. Secondary concerns may still be present; for example, a command may return a read model or call an external integration.

## `command`

Use for stakeholder actions whose primary outcome changes domain or persistent application state.

Required:

- at least one `state_changes` entry;
- mutation rules, authorization, idempotency, concurrency, and observable result;
- acceptance coverage for every state change and material failure.

Examples: cancel an order, approve a request, change an address.

## `query`

Use for stakeholder actions whose primary outcome reads or derives information without changing domain state.

Required:

- at least one `read_models` entry;
- zero `state_changes` entries;
- explicit source-to-field mapping, freshness, filtering, sorting, pagination, empty-result behavior, and authorization filtering;
- interface outputs that expose the complete result contract.

Cache population, telemetry, and audit may be represented as `side_effects`; they must not alter domain behavior.

Examples: show a dashboard, search orders, export a report.

## `process`

Use for multi-step behavior whose primary concern is progression through an ordered workflow, including asynchronous or long-running work.

Required:

- at least two `process_steps` entries;
- unique step order values;
- explicit state before and after each step;
- failure behavior for each step;
- at least one terminal step;
- recovery, cancellation, timeout, and compensation decisions where applicable.

Examples: onboarding workflow, document review, fulfillment pipeline.

## `integration`

Use when the primary deliverable is a boundary with an external system or independently deployed service.

Required:

- at least one `integration_contracts` entry;
- direction, transport, request and response contracts;
- idempotency, timeout, retry, failure mapping, and compatibility/versioning rules;
- acceptance coverage for success and material remote failures.

Examples: consume a partner webhook, send invoices to an accounting provider, synchronize inventory.

## Classification rule

Classify by the stakeholder-visible primary outcome, not by implementation technology:

1. State mutation is primary -> `command`.
2. Information retrieval is primary -> `query`.
3. Ordered progression is primary -> `process`.
4. External boundary behavior is primary -> `integration`.

When two outcomes are independently deployable or independently valuable, model two slices instead of combining types.
