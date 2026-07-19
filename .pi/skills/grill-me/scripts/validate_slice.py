#!/usr/bin/env python3
"""Validate a slice.json v2 against schema and semantic rules."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from typing import Any, Iterable

try:
    from jsonschema import Draft202012Validator
except ImportError as exc:
    raise SystemExit("Missing dependency 'jsonschema'. Install: pip install jsonschema") from exc

PLACEHOLDER_RE = re.compile(
    r"\b(?:tbd|todo|unknown|later|to be decided|not specified|fixme)\b",
    re.IGNORECASE,
)

_QUOTED_RE = re.compile(r"`[^`]*`|\"[^\"]*\"")


def load_schema(schema_path: Path = None) -> dict:
    """Load schema from default location or provided path."""
    if schema_path is None:
        schema_path = Path(__file__).parent.parent / "schema.json"
    try:
        return json.loads(schema_path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise SystemExit(f"Schema not found: {schema_path}") from exc


def _unquoted_placeholder(text: str) -> re.Match | None:
    """Find placeholder tokens outside quotes."""
    cleaned = _QUOTED_RE.sub(lambda m: " " * len(m.group()), text)
    return PLACEHOLDER_RE.search(cleaned)


def load_json(path: Path) -> Any:
    """Load JSON file."""
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise SystemExit(f"File not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise SystemExit(
            f"Invalid JSON in {path}: line {exc.lineno}, column {exc.colno}: {exc.msg}"
        ) from exc


def json_path(parts: Iterable[Any]) -> str:
    """Format a JSON path from parts."""
    result = "$"
    for part in parts:
        if isinstance(part, int):
            result += f"[{part}]"
        else:
            result += f".{part}"
    return result


def walk_strings(value: Any, path: tuple[Any, ...] = ()) -> Iterable[tuple[tuple[Any, ...], str]]:
    """Walk all strings in a nested structure."""
    if isinstance(value, str):
        yield path, value
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from walk_strings(item, path + (index,))
    elif isinstance(value, dict):
        for key, item in value.items():
            yield from walk_strings(item, path + (key,))


def semantic_errors(document: dict[str, Any]) -> list[str]:
    """Check semantic rules beyond structural schema validation."""
    errors: list[str] = []

    readiness = document.get("readiness")
    blockers = document.get("blockers", [])

    # Rule 1: ready slices must have empty blockers
    if readiness == "ready" and blockers:
        errors.append(f"A ready slice requires $.blockers to be empty, but found {len(blockers)} blocker(s)")

    # Rule 2: blocked slices must have at least one blocker
    if readiness == "blocked" and not blockers:
        errors.append("A blocked slice must have at least one blocker (question, assumption, or blocker)")

    # Rule 3: no unquoted placeholder tokens in ready slices
    if readiness == "ready":
        for path, text in walk_strings(document):
            m = _unquoted_placeholder(text)
            if m:
                token = m.group()
                errors.append(
                    f"Unresolved placeholder token {token!r} in ready slice at {json_path(path)} "
                    f"(wrap verbatim quotes in backticks to ignore)"
                )

    # Rule 4: if security_relevant=true, security field must be present and non-empty
    if document.get("security_relevant") is True:
        security = document.get("security", "").strip()
        if not security:
            errors.append("If $.security_relevant=true, $.security must be present and non-empty")

    # Rule 5: every interfaces[].errors[].code must appear verbatim in at least one
    # business_rules entry (rule or outcome) or decision_tables row then value.
    interfaces = document.get("interfaces", [])
    if isinstance(interfaces, list) and interfaces:
        reference_texts: list[str] = []
        for rule in document.get("business_rules", []):
            if isinstance(rule, dict):
                for field in ("rule", "outcome"):
                    val = rule.get(field)
                    if isinstance(val, str):
                        reference_texts.append(val)
        for table in document.get("decision_tables", []):
            if isinstance(table, dict):
                for row in table.get("rows", []):
                    if isinstance(row, dict):
                        for _, text in walk_strings(row.get("then", {})):
                            reference_texts.append(text)
        for i, iface in enumerate(interfaces):
            if not isinstance(iface, dict):
                continue
            for j, err_entry in enumerate(iface.get("errors", [])):
                if not isinstance(err_entry, dict):
                    continue
                code = err_entry.get("code")
                if not isinstance(code, str):
                    continue
                if not any(code in ref for ref in reference_texts):
                    errors.append(
                        f"Unreferenced error code {code} at $.interfaces[{i}].errors[{j}].code"
                    )

    return errors


def full_validate(document: dict[str, Any], schema: dict = None) -> list[str]:
    """Run structural + semantic validation. Returns list of all errors (empty = valid)."""
    if schema is None:
        schema = load_schema()

    # Structural validation
    try:
        validator = Draft202012Validator(schema)
    except Exception as exc:
        return [f"Invalid schema: {exc}"]

    structural = sorted(validator.iter_errors(document), key=lambda err: list(err.absolute_path))
    structural_errors = [
        f"{json_path(e.absolute_path)}: {e.message}"
        for e in structural
    ]

    # Semantic validation (only if structural is clean)
    semantic = semantic_errors(document) if not structural_errors else []

    return structural_errors + semantic


def main() -> int:
    """CLI entry point: validate <schema.json> <slice.json>"""
    if len(sys.argv) != 3:
        print(
            "Usage: python validate_slice.py <schema.json> <slice.json>",
            file=sys.stderr,
        )
        return 2

    schema_path = Path(sys.argv[1])
    document_path = Path(sys.argv[2])

    schema = load_json(schema_path)
    document = load_json(document_path)

    # Check schema validity
    try:
        Draft202012Validator.check_schema(schema)
    except Exception as exc:
        print(f"Invalid schema {schema_path}: {exc}", file=sys.stderr)
        return 2

    # Run full validation
    errors = full_validate(document, schema)

    if errors:
        print("Validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(f"✓ Validation passed: {document_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
