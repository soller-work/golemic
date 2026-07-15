#!/usr/bin/env python3
"""Validate an implementation slice structurally and semantically."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from typing import Any, Iterable

try:
    from jsonschema import Draft202012Validator
except ImportError as exc:  # pragma: no cover
    raise SystemExit(
        "Missing dependency 'jsonschema'. Install it with: python -m pip install jsonschema"
    ) from exc

PLACEHOLDER_RE = re.compile(
    r"\b(?:tbd|todo|unknown|later|to be decided|not specified|fixme)\b",
    re.IGNORECASE,
)

ID_COLLECTIONS = (
    "business_rules",
    "decision_tables",
    "interfaces",
    "read_models",
    "process_steps",
    "integration_contracts",
    "state_changes",
    "side_effects",
    "acceptance_scenarios",
    "decision_log",
    "codebase_evidence",
)

TRACE_COLLECTIONS = (
    "business_rules",
    "decision_tables",
    "interfaces",
    "read_models",
    "process_steps",
    "integration_contracts",
    "state_changes",
    "side_effects",
)


def load_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise SystemExit(f"File not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise SystemExit(
            f"Invalid JSON in {path}: line {exc.lineno}, column {exc.colno}: {exc.msg}"
        ) from exc


def json_path(parts: Iterable[Any]) -> str:
    result = "$"
    for part in parts:
        if isinstance(part, int):
            result += f"[{part}]"
        else:
            result += f".{part}"
    return result


def walk_strings(value: Any, path: tuple[Any, ...] = ()) -> Iterable[tuple[tuple[Any, ...], str]]:
    if isinstance(value, str):
        yield path, value
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from walk_strings(item, path + (index,))
    elif isinstance(value, dict):
        for key, item in value.items():
            yield from walk_strings(item, path + (key,))


def collect_ids(document: dict[str, Any]) -> tuple[dict[str, str], list[str]]:
    locations: dict[str, str] = {}
    errors: list[str] = []
    for collection in ID_COLLECTIONS:
        for index, item in enumerate(document.get(collection, [])):
            item_id = item.get("id")
            if not isinstance(item_id, str):
                continue
            location = f"$.{collection}[{index}].id"
            if item_id in locations:
                errors.append(
                    f"Duplicate identifier {item_id!r} at {location}; first defined at {locations[item_id]}"
                )
            else:
                locations[item_id] = location
    return locations, errors


def semantic_errors(document: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    ids, duplicate_errors = collect_ids(document)
    errors.extend(duplicate_errors)

    traceable_ids = {
        item["id"]
        for collection in TRACE_COLLECTIONS
        for item in document.get(collection, [])
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }
    evidence_ids = {
        item["id"]
        for item in document.get("codebase_evidence", [])
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }
    business_rule_ids = {
        item["id"]
        for item in document.get("business_rules", [])
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }

    traced_ids: set[str] = set()
    for index, scenario in enumerate(document.get("acceptance_scenarios", [])):
        for trace_id in scenario.get("traces_to", []):
            traced_ids.add(trace_id)
            if trace_id not in traceable_ids:
                errors.append(
                    f"Unknown trace reference {trace_id!r} at $.acceptance_scenarios[{index}].traces_to"
                )

    for trace_id in sorted(traceable_ids - traced_ids):
        errors.append(f"Traceable item {trace_id!r} is not covered by any acceptance scenario")

    for table_index, table in enumerate(document.get("decision_tables", [])):
        for row_index, row in enumerate(table.get("rows", [])):
            for rule_id in row.get("rule_ids", []):
                if rule_id not in business_rule_ids:
                    errors.append(
                        f"Unknown business-rule reference {rule_id!r} at "
                        f"$.decision_tables[{table_index}].rows[{row_index}].rule_ids"
                    )

    slice_type = document.get("slice_type")
    state_changes = document.get("state_changes", [])
    read_models = document.get("read_models", [])
    process_steps = document.get("process_steps", [])
    integration_contracts = document.get("integration_contracts", [])

    if slice_type == "command" and not state_changes:
        errors.append("A command slice requires at least one state change")
    elif slice_type == "query":
        if not read_models:
            errors.append("A query slice requires at least one read model")
        if state_changes:
            errors.append("A query slice must not contain domain state changes")
    elif slice_type == "process":
        if len(process_steps) < 2:
            errors.append("A process slice requires at least two ordered process steps")
        step_orders = [step.get("order") for step in process_steps]
        if len(step_orders) != len(set(step_orders)):
            errors.append("Process-step order values must be unique")
        if process_steps and not any(step.get("terminal") is True for step in process_steps):
            errors.append("A process slice requires at least one terminal process step")
    elif slice_type == "integration" and not integration_contracts:
        errors.append("An integration slice requires at least one integration contract")

    referenced_evidence: set[str] = set()
    for index, decision in enumerate(document.get("decision_log", [])):
        decision_evidence = decision.get("evidence_ids", [])
        for evidence_id in decision_evidence:
            referenced_evidence.add(evidence_id)
            if evidence_id not in evidence_ids:
                errors.append(
                    f"Unknown evidence reference {evidence_id!r} at $.decision_log[{index}].evidence_ids"
                )
        if decision.get("source") == "codebase" and not decision_evidence:
            errors.append(
                f"Codebase-sourced decision at $.decision_log[{index}] must reference at least one evidence item"
            )

    readiness = document.get("readiness")
    unresolved_lists = (
        "open_questions",
        "assumptions_requiring_confirmation",
        "blockers",
    )

    if readiness == "ready":
        for field in unresolved_lists:
            if document.get(field):
                errors.append(f"A ready slice requires $.{field} to be empty")

        for path, text in walk_strings(document):
            if PLACEHOLDER_RE.search(text):
                errors.append(
                    f"Unresolved placeholder in ready slice at {json_path(path)}: {text!r}"
                )

        unreferenced_evidence = evidence_ids - referenced_evidence
        for evidence_id in sorted(unreferenced_evidence):
            errors.append(
                f"Codebase evidence {evidence_id!r} is not referenced by any decision"
            )

        for index, evidence in enumerate(document.get("codebase_evidence", [])):
            if evidence.get("verified") is not True:
                errors.append(
                    f"Ready slice requires verified codebase evidence at $.codebase_evidence[{index}]"
                )

    if readiness == "blocked" and not any(document.get(field) for field in unresolved_lists):
        errors.append(
            "A blocked slice must contain at least one open question, unconfirmed assumption, or blocker"
        )

    return errors


def main() -> int:
    if len(sys.argv) != 3:
        print(
            "Usage: python scripts/validate_slice.py <schema.json> <implementation-slice.json>",
            file=sys.stderr,
        )
        return 2

    schema_path = Path(sys.argv[1])
    document_path = Path(sys.argv[2])
    schema = load_json(schema_path)
    document = load_json(document_path)

    try:
        Draft202012Validator.check_schema(schema)
    except Exception as exc:
        print(f"Invalid schema {schema_path}: {exc}", file=sys.stderr)
        return 2

    validator = Draft202012Validator(schema)
    structural = sorted(validator.iter_errors(document), key=lambda err: list(err.absolute_path))
    semantic = semantic_errors(document) if not structural else []

    if structural or semantic:
        print("Validation failed:", file=sys.stderr)
        for error in structural:
            print(f"- {json_path(error.absolute_path)}: {error.message}", file=sys.stderr)
        for error in semantic:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(f"Validation passed: {document_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
