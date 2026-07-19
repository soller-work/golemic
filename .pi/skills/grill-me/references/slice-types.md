# Slice Types v2

Choose exactly one primary `slice_type`. This determines the focus of the `behavior` Markdown field.

## `command`

Stakeholder actions whose primary outcome is a state mutation (create, update, delete, or state transition).

**behavior focus**: Describe the state mutations, preconditions, invariants, idempotency rules, and observable results.

Examples: approve a request, cancel an order, update a user profile.

## `query`

Stakeholder actions whose primary outcome is information retrieval without domain state changes.

**behavior focus**: Describe the read model shape, source-to-field mapping, filtering/sorting/pagination, freshness guarantees, empty-result handling, and authorization rules.

Examples: fetch a dashboard, search records, export a report.

## `process`

Multi-step workflows whose primary concern is progression through ordered stages, including async or long-running work.

**behavior focus**: Describe each ordered step, state transitions, terminal conditions, failure handling per step, and compensation/cancellation/timeout logic.

Examples: onboarding workflow, document review pipeline, fulfillment.

## `integration`

Boundary behavior with external systems or independently deployed services.

**behavior focus**: Describe the external contract (direction, transport, request/response shape), idempotency, timeout/retry, failure mappings, and version compatibility.

Examples: consume a webhook, sync inventory, send invoices to accounting.

## Classification Rule

Classify by the stakeholder-visible primary outcome:
1. State mutation → `command`
2. Information retrieval → `query`
3. Ordered progression → `process`
4. External boundary → `integration`

When outcomes are independently valuable, model separate slices.
