#!/usr/bin/env python3
"""Create, inspect, validate, and commit implementation-slice issue sets."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Iterable

try:
    from jsonschema import Draft202012Validator
except ImportError as exc:
    raise SystemExit(
        "Missing dependency 'jsonschema'. Install it with: python -m pip install jsonschema"
    ) from exc

PLACEHOLDER_RE = re.compile(
    r"\b(?:tbd|todo|unknown|later|to be decided|not specified|fixme|"
    r"fill[_ -]?in|placeholder|change[_ -]?me)\b",
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
        raise ValueError(f"File not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(
            f"Invalid JSON in {path}: line {exc.lineno}, column {exc.colno}: {exc.msg}"
        ) from exc


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def slugify(text: str) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")
    if not slug:
        raise ValueError("Title produces an empty slug")
    return slug


def json_path(parts: Iterable[Any]) -> str:
    result = "$"
    for part in parts:
        result += f"[{part}]" if isinstance(part, int) else f".{part}"
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
            item_id = item.get("id") if isinstance(item, dict) else None
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


def semantic_errors(document: dict[str, Any], repo_root: Path) -> list[str]:
    errors: list[str] = []
    _, duplicate_errors = collect_ids(document)
    errors.extend(duplicate_errors)

    for path, text in walk_strings(document):
        if PLACEHOLDER_RE.search(text):
            errors.append(f"Unresolved placeholder at {json_path(path)}: {text!r}")

    traceable_ids = {
        item["id"]
        for collection in TRACE_COLLECTIONS
        for item in document.get(collection, [])
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }
    business_rule_ids = {
        item["id"]
        for item in document.get("business_rules", [])
        if isinstance(item, dict) and isinstance(item.get("id"), str)
    }
    evidence_ids = {
        item["id"]
        for item in document.get("codebase_evidence", [])
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
        errors.append(f"Traceable item {trace_id!r} is not covered by an acceptance scenario")

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
            errors.append("A process slice requires at least two process steps")
        orders = [step.get("order") for step in process_steps]
        if len(orders) != len(set(orders)):
            errors.append("Process-step order values must be unique")
        if process_steps and not any(step.get("terminal") is True for step in process_steps):
            errors.append("A process slice requires at least one terminal step")
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
                f"Codebase-sourced decision at $.decision_log[{index}] requires evidence"
            )

    for index, evidence in enumerate(document.get("codebase_evidence", [])):
        raw_path = evidence.get("path")
        if evidence.get("verified") is True and isinstance(raw_path, str):
            evidence_path = (repo_root / raw_path).resolve()
            try:
                evidence_path.relative_to(repo_root.resolve())
            except ValueError:
                errors.append(
                    f"Evidence path escapes repository at $.codebase_evidence[{index}].path"
                )
            else:
                if not evidence_path.exists():
                    errors.append(
                        f"Verified evidence path does not exist: {raw_path!r}"
                    )

    unresolved_fields = (
        "open_questions",
        "assumptions_requiring_confirmation",
        "blockers",
    )
    readiness = document.get("readiness")

    if readiness == "ready":
        for field in unresolved_fields:
            if document.get(field):
                errors.append(f"A ready slice requires $.{field} to be empty")
        for evidence_id in sorted(evidence_ids - referenced_evidence):
            errors.append(f"Codebase evidence {evidence_id!r} is not referenced by a decision")
        for index, evidence in enumerate(document.get("codebase_evidence", [])):
            if evidence.get("verified") is not True:
                errors.append(
                    f"Ready slice requires verified evidence at $.codebase_evidence[{index}]"
                )
    elif readiness == "blocked":
        if not any(document.get(field) for field in unresolved_fields):
            errors.append(
                "A blocked slice requires an open question, unconfirmed assumption, or blocker"
            )

    return errors


def validate_document(schema_path: Path, document_path: Path, repo_root: Path) -> tuple[dict[str, Any] | None, list[str]]:
    try:
        schema = load_json(schema_path)
        document = load_json(document_path)
    except ValueError as exc:
        return None, [str(exc)]

    try:
        Draft202012Validator.check_schema(schema)
    except Exception as exc:
        return None, [f"Invalid schema {schema_path}: {exc}"]

    structural = sorted(
        Draft202012Validator(schema).iter_errors(document),
        key=lambda err: list(err.absolute_path),
    )
    if structural:
        return document, [
            f"{json_path(error.absolute_path)}: {error.message}" for error in structural
        ]

    return document, semantic_errors(document, repo_root)


def base_scaffold(slice_type: str, title: str) -> dict[str, Any]:
    slice_id = slugify(title)
    document: dict[str, Any] = {
        "schema_version": "1.1.0",
        "slice_type": slice_type,
        "slice_id": slice_id,
        "title": title,
        "summary": "FILL_IN",
        "readiness": "blocked",
        "stakeholder_intent": {
            "actor": "FILL_IN",
            "goal": "FILL_IN",
            "business_value": "FILL_IN",
            "trigger": "FILL_IN",
            "success_outcome": "FILL_IN",
        },
        "scope": {"in_scope": ["FILL_IN"], "out_of_scope": []},
        "preconditions": ["FILL_IN"],
        "business_rules": [
            {
                "id": "BR-001",
                "rule": "FILL_IN",
                "applies_when": "FILL_IN",
                "outcome": "FILL_IN",
            }
        ],
        "decision_tables": [],
        "interfaces": [
            {
                "id": "IF-001",
                "kind": "internal_api",
                "name": "FILL_IN",
                "operation": "FILL_IN",
                "inputs": [],
                "outputs": [
                    {"condition": "FILL_IN", "status": "FILL_IN", "body": "FILL_IN"}
                ],
                "errors": [],
            }
        ],
        "read_models": [],
        "process_steps": [],
        "integration_contracts": [],
        "state_changes": [],
        "side_effects": [],
        "security": {
            "authentication": "FILL_IN",
            "authorization_rules": ["FILL_IN"],
            "data_classification": "FILL_IN",
            "audit_requirements": [],
        },
        "implementation_context": {
            "target_modules": ["FILL_IN"],
            "integration_points": ["FILL_IN"],
            "architecture_constraints": ["FILL_IN"],
            "allowed_changes": ["FILL_IN"],
            "forbidden_changes": [],
            "dependencies": [],
            "migration_strategy": "FILL_IN",
            "consistency_and_concurrency": "FILL_IN",
        },
        "acceptance_scenarios": [
            {
                "id": "AC-001",
                "title": "FILL_IN",
                "given": ["FILL_IN"],
                "when": ["FILL_IN"],
                "then": ["FILL_IN"],
                "traces_to": ["BR-001", "IF-001"],
            }
        ],
        "quality": {
            "required_test_levels": ["unit"],
            "test_cases": ["FILL_IN"],
            "quality_commands": ["FILL_IN"],
            "non_functional_requirements": [],
            "definition_of_done": ["FILL_IN"],
        },
        "decision_log": [
            {
                "id": "D-001",
                "topic": "FILL_IN",
                "decision": "FILL_IN",
                "source": "user",
                "evidence_ids": [],
                "rationale": "FILL_IN",
            }
        ],
        "codebase_evidence": [],
        "open_questions": [],
        "assumptions_requiring_confirmation": [],
        "blockers": ["Complete the interview and repository inspection"],
    }

    if slice_type == "command":
        document["state_changes"] = [
            {
                "id": "SC-001",
                "target": "FILL_IN",
                "precondition": "FILL_IN",
                "changes": [
                    {"field": "FILL_IN", "operation": "set", "value_source": "FILL_IN"}
                ],
            }
        ]
        document["acceptance_scenarios"][0]["traces_to"].append("SC-001")
    elif slice_type == "query":
        document["read_models"] = [
            {
                "id": "RM-001",
                "name": "FILL_IN",
                "purpose": "FILL_IN",
                "sources": ["FILL_IN"],
                "fields": [
                    {
                        "name": "FILL_IN",
                        "type": "FILL_IN",
                        "source": "FILL_IN",
                        "nullable": False,
                        "meaning": "FILL_IN",
                    }
                ],
                "freshness": "FILL_IN",
                "filtering_sorting_pagination": "FILL_IN",
                "empty_result": "FILL_IN",
                "authorization_filtering": "FILL_IN",
            }
        ]
        document["acceptance_scenarios"][0]["traces_to"].append("RM-001")
    elif slice_type == "process":
        document["process_steps"] = [
            {
                "id": "PS-001",
                "order": 1,
                "name": "FILL_IN",
                "trigger": "FILL_IN",
                "action": "FILL_IN",
                "state_before": "FILL_IN",
                "state_after": "FILL_IN",
                "outcome": "FILL_IN",
                "failure_behavior": "FILL_IN",
                "terminal": False,
            },
            {
                "id": "PS-002",
                "order": 2,
                "name": "FILL_IN",
                "trigger": "FILL_IN",
                "action": "FILL_IN",
                "state_before": "FILL_IN",
                "state_after": "FILL_IN",
                "outcome": "FILL_IN",
                "failure_behavior": "FILL_IN",
                "terminal": True,
            },
        ]
        document["acceptance_scenarios"][0]["traces_to"].extend(["PS-001", "PS-002"])
    elif slice_type == "integration":
        document["integration_contracts"] = [
            {
                "id": "IC-001",
                "external_system": "FILL_IN",
                "direction": "outbound",
                "transport": "FILL_IN",
                "operation": "FILL_IN",
                "request_contract": "FILL_IN",
                "response_contract": "FILL_IN",
                "idempotency": "FILL_IN",
                "timeout": "FILL_IN",
                "retry_policy": "FILL_IN",
                "failure_mapping": "FILL_IN",
                "compatibility": "FILL_IN",
            }
        ]
        document["acceptance_scenarios"][0]["traces_to"].append("IC-001")

    return document


def next_target(backlog_dir: Path, title: str) -> Path:
    slug = slugify(title)
    numbers = []
    if backlog_dir.exists():
        for path in backlog_dir.glob("[0-9][0-9][0-9]_*.json"):
            try:
                numbers.append(int(path.name[:3]))
            except ValueError:
                pass
    return backlog_dir / f"{max(numbers, default=0) + 1:03d}_{slug}.json"


def cmd_new(args: argparse.Namespace) -> int:
    schema_path = Path(args.schema).resolve()
    schema = load_json(schema_path)
    Draft202012Validator.check_schema(schema)

    target = next_target(Path(args.dir), args.title)
    if target.exists():
        raise ValueError(f"Refusing to overwrite: {target}")

    document = base_scaffold(args.slice_type, args.title)
    structural = list(Draft202012Validator(schema).iter_errors(document))
    if structural:
        first = structural[0]
        raise ValueError(f"Internal scaffold error at {json_path(first.absolute_path)}: {first.message}")

    write_json(target, document)
    print(target)
    return 0


def resolve_local_ref(schema: dict[str, Any], node: dict[str, Any]) -> dict[str, Any]:
    seen: set[str] = set()
    current = node
    while isinstance(current, dict) and isinstance(current.get("$ref"), str):
        ref = current["$ref"]
        if not ref.startswith("#/") or ref in seen:
            break
        seen.add(ref)
        target: Any = schema
        for token in ref[2:].split("/"):
            token = token.replace("~1", "/").replace("~0", "~")
            target = target[token]
        if not isinstance(target, dict):
            break
        current = target
    return current


def schema_node(schema: dict[str, Any], field_path: str) -> dict[str, Any]:
    current: dict[str, Any] = schema
    for raw_part in field_path.split("."):
        current = resolve_local_ref(schema, current)
        is_array = raw_part.endswith("[]")
        part = raw_part[:-2] if is_array else raw_part
        properties = current.get("properties")
        if not isinstance(properties, dict) or part not in properties:
            raise ValueError(f"Field not found: {part}")
        current = properties[part]
        current = resolve_local_ref(schema, current)
        if is_array:
            if current.get("type") != "array" or not isinstance(current.get("items"), dict):
                raise ValueError(f"Field is not an array: {part}")
            current = resolve_local_ref(schema, current["items"])
    return current


def cmd_describe(args: argparse.Namespace) -> int:
    schema = load_json(Path(args.schema))
    node = schema_node(schema, args.path)
    summary = {
        key: node[key]
        for key in (
            "type",
            "const",
            "enum",
            "pattern",
            "minItems",
            "maxItems",
            "required",
            "properties",
            "items",
        )
        if key in node
    }
    print(json.dumps(summary, indent=2))
    return 0


def resolve_file_path(repo_root: Path, raw_path: str) -> Path:
    path = Path(raw_path)
    return path.resolve() if path.is_absolute() else (repo_root / path).resolve()


def validate_issue_set(
    schema_path: Path,
    file_paths: list[Path],
    repo_root: Path,
) -> tuple[list[dict[str, Any]], list[str]]:
    documents: list[dict[str, Any]] = []
    errors: list[str] = []
    seen_paths: set[Path] = set()
    seen_slice_ids: dict[str, Path] = {}

    for file_path in file_paths:
        if file_path in seen_paths:
            errors.append(f"Duplicate target file: {file_path}")
            continue
        seen_paths.add(file_path)

        document, document_errors = validate_document(schema_path, file_path, repo_root)
        if document_errors:
            errors.extend(f"{file_path}: {error}" for error in document_errors)
            continue
        assert document is not None
        documents.append(document)

        slice_id = document.get("slice_id")
        if isinstance(slice_id, str):
            previous = seen_slice_ids.get(slice_id)
            if previous is not None:
                errors.append(
                    f"Duplicate slice_id {slice_id!r} in {previous} and {file_path}"
                )
            else:
                seen_slice_ids[slice_id] = file_path

    return documents, errors


def cmd_validate(args: argparse.Namespace) -> int:
    repo_root = Path(args.repo_root).resolve()
    file_paths = [resolve_file_path(repo_root, raw) for raw in args.files]
    _, errors = validate_issue_set(
        Path(args.schema).resolve(), file_paths, repo_root
    )
    if errors:
        print("Validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    for file_path in file_paths:
        print(f"Validation passed: {file_path}")
    return 0


def run_git(repo_root: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        ["git", "-C", str(repo_root), *args],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if check and result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or "git command failed"
        raise ValueError(detail)
    return result


def relative_repo_path(repo_root: Path, file_path: Path) -> str:
    try:
        relative = file_path.resolve().relative_to(repo_root.resolve())
    except ValueError as exc:
        raise ValueError("Target file is outside the repository") from exc
    normalized = relative.as_posix()
    if not re.fullmatch(r"docs/backlog/[0-9]{3}_[a-z0-9]+(?:-[a-z0-9]+)*\.json", normalized):
        raise ValueError("Target must match docs/backlog/NNN_<slug>.json")
    return normalized


def nul_paths(value: str) -> list[str]:
    return [item for item in value.split("\0") if item]


def cmd_commit(args: argparse.Namespace) -> int:
    repo_root = Path(args.repo_root).resolve()
    file_paths = [resolve_file_path(repo_root, raw) for raw in args.files]
    relatives = [relative_repo_path(repo_root, path) for path in file_paths]

    if len(relatives) != len(set(relatives)):
        print("Commit blocked: duplicate target file", file=sys.stderr)
        return 1

    documents, errors = validate_issue_set(
        Path(args.schema).resolve(), file_paths, repo_root
    )
    if errors:
        print("Validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    blocked = [
        document["slice_id"]
        for document in documents
        if document.get("readiness") != "ready"
    ]
    if blocked:
        print(
            "Commit blocked: readiness is not ready for " + ", ".join(blocked),
            file=sys.stderr,
        )
        return 1

    run_git(repo_root, "rev-parse", "--is-inside-work-tree")

    target_set = set(relatives)
    staged_before = set(
        nul_paths(run_git(repo_root, "diff", "--cached", "--name-only", "-z").stdout)
    )
    foreign_staged = sorted(staged_before - target_set)
    if foreign_staged:
        print(
            "Commit blocked: other staged files: " + ", ".join(foreign_staged),
            file=sys.stderr,
        )
        return 1

    tracked = [
        run_git(repo_root, "ls-files", "--error-unmatch", "--", relative, check=False).returncode
        == 0
        for relative in relatives
    ]
    run_git(repo_root, "add", "--", *relatives)

    staged_after = set(
        nul_paths(run_git(repo_root, "diff", "--cached", "--name-only", "-z").stdout)
    )
    if staged_after != target_set:
        missing = sorted(target_set - staged_after)
        extra = sorted(staged_after - target_set)
        details: list[str] = []
        if missing:
            details.append("missing " + ", ".join(missing))
        if extra:
            details.append("unexpected " + ", ".join(extra))
        print("Commit blocked: staged set " + "; ".join(details), file=sys.stderr)
        return 1

    if len(documents) == 1:
        verb = "update" if tracked[0] else "add"
        message = (
            f"docs(backlog): {verb} {documents[0]['slice_id']} implementation slice"
        )
    else:
        verb = "update" if all(tracked) else "add" if not any(tracked) else "revise"
        message = f"docs(backlog): {verb} {len(documents)} implementation slices"

    run_git(repo_root, "commit", "-m", message)

    commit_hash = run_git(repo_root, "rev-parse", "--short", "HEAD").stdout.strip()
    committed_paths = sorted(
        line
        for line in run_git(
            repo_root,
            "diff-tree",
            "--root",
            "--no-commit-id",
            "--name-only",
            "-r",
            "HEAD",
        ).stdout.splitlines()
        if line
    )
    if committed_paths != sorted(relatives):
        print(
            "Commit completed but contained unexpected paths: "
            + ", ".join(committed_paths),
            file=sys.stderr,
        )
        return 1

    for relative in relatives:
        print(relative)
    print("Validation passed")
    print(commit_hash)
    return 0

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    new = subparsers.add_parser("new", help="Create the next backlog scaffold")
    new.add_argument("--schema", required=True)
    new.add_argument("--slice-type", required=True, choices=("command", "query", "process", "integration"))
    new.add_argument("--title", required=True)
    new.add_argument("--dir", default="docs/backlog")
    new.set_defaults(func=cmd_new)

    describe = subparsers.add_parser("describe", help="Describe a schema field")
    describe.add_argument("--schema", required=True)
    describe.add_argument("--path", required=True)
    describe.set_defaults(func=cmd_describe)

    validate = subparsers.add_parser("validate", help="Validate an issue set")
    validate.add_argument("--schema", required=True)
    validate.add_argument("--file", dest="files", action="append", required=True)
    validate.add_argument("--repo-root", default=".")
    validate.set_defaults(func=cmd_validate)

    commit = subparsers.add_parser("commit", help="Validate and commit only the target issue set")
    commit.add_argument("--schema", required=True)
    commit.add_argument("--file", dest="files", action="append", required=True)
    commit.add_argument("--repo-root", default=".")
    commit.set_defaults(func=cmd_commit)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        return args.func(args)
    except (ValueError, KeyError, OSError) as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
