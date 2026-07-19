#!/usr/bin/env python3
"""Driver script for grill-me slice workflow. Subcommands: init, plan, set, status, finalize."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

try:
    from jsonschema import Draft202012Validator
except ImportError as exc:
    raise SystemExit("Missing dependency 'jsonschema'. Install: python -m pip install jsonschema") from exc

SCHEMA_PATH = Path(__file__).parent.parent / "schema.json"
VALIDATE_SLICE = Path(__file__).parent / "validate_slice.py"

# Section → (id_prefix, is_array, is_object)
ARRAY_SECTIONS = {
    "business_rules": "BR",
    "decision_tables": "DT",
    "interfaces": "IF",
    "read_models": "RM",
    "process_steps": "PS",
    "integration_contracts": "IC",
    "state_changes": "SC",
    "side_effects": "SE",
    "acceptance_scenarios": "AC",
    "codebase_evidence": "EV",
    "decision_log": "D",
}

OBJECT_SECTIONS = {"stakeholder_intent", "scope", "security", "implementation_context", "quality"}

SCALAR_SECTIONS = {
    "title", "summary", "risk", "readiness", "preconditions",
    "open_questions", "assumptions_requiring_confirmation", "blockers", "depends_on",
}

PLAN_ORDER = [
    "stakeholder_intent",
    "scope",
    "preconditions",
    "business_rules",
    "decision_tables",
    "interfaces",
    "read_models",
    "process_steps",
    "integration_contracts",
    "state_changes",
    "side_effects",
    "security",
    "implementation_context",
    "acceptance_scenarios",
    "quality",
    "decision_log",
    "codebase_evidence",
    "open_questions",
    "assumptions_requiring_confirmation",
    "blockers",
]

PLAN_HINTS = {
    "stakeholder_intent": "Provide a single object with keys: actor, goal, business_value, trigger, success_outcome. All non-empty strings.",
    "scope": "Provide {in_scope: [...], out_of_scope: [...]}. in_scope must have ≥1 item.",
    "preconditions": "Provide a non-empty array of non-empty strings.",
    "business_rules": "Provide array of {rule, applies_when, outcome} objects. IDs (BR-001..N) are assigned by driver.",
    "decision_tables": "Provide array of {name, inputs, rows, default_outcome} objects. IDs (DT-001..N) assigned by driver. rows: [{when:{}, then:{}, rule_ids:[BR-xxx]}].",
    "interfaces": "Provide array of {kind, name, operation, inputs, outputs, errors} objects. IDs (IF-001..N) assigned by driver. kind ∈ ui|http_api|internal_api|cli|event|scheduled_job|other.",
    "read_models": "Provide array of {name, purpose, sources, fields, freshness, filtering_sorting_pagination, empty_result, authorization_filtering}. IDs (RM-001..N) assigned by driver.",
    "process_steps": "Provide array of {order, name, trigger, action, state_before, state_after, outcome, failure_behavior, terminal}. IDs (PS-001..N) assigned by driver.",
    "integration_contracts": "Provide array of {external_system, direction, transport, operation, request_contract, response_contract, idempotency, timeout, retry_policy, failure_mapping, compatibility}. IDs (IC-001..N) assigned by driver.",
    "state_changes": "Provide array of {target, precondition, changes:[{field, operation, value_source}]}. IDs (SC-001..N) assigned by driver. operation ∈ set|clear|increment|decrement|append|remove|create|delete.",
    "side_effects": "Provide array of {type, description, delivery, idempotency, failure_policy}. IDs (SE-001..N) assigned by driver. type ∈ domain_event|integration_event|notification|external_call|payment|audit|cache|other. delivery ∈ synchronous|asynchronous.",
    "security": "Provide {authentication, authorization_rules:[...], data_classification, audit_requirements:[...]}.",
    "implementation_context": "Provide {target_modules, integration_points, architecture_constraints, allowed_changes, forbidden_changes, dependencies, migration_strategy, consistency_and_concurrency}.",
    "acceptance_scenarios": "Provide array of {title, given:[...], when:[...], then:[...], traces_to:[BR-xxx|DT-xxx|...]}. IDs (AC-001..N) assigned by driver. traces_to must reference existing IDs.",
    "quality": "Provide {required_test_levels:[unit|component|integration|contract|end_to_end|migration|security], test_cases:[...], quality_commands:[...], non_functional_requirements:[...], definition_of_done:[...]}.",
    "decision_log": "Provide array of {topic, decision, source, evidence_ids:[EV-xxx], rationale}. IDs (D-001..N) assigned by driver. source ∈ user|codebase|confirmed_recommendation.",
    "codebase_evidence": "Provide array of {path, symbol, location, finding, verified:bool}. IDs (EV-001..N) assigned by driver.",
    "open_questions": "Provide array of strings (or [] if none). Non-empty → readiness stays blocked.",
    "assumptions_requiring_confirmation": "Provide array of strings (or [] if none). Non-empty → readiness stays blocked.",
    "blockers": "Provide array of strings (or [] if none). Non-empty → readiness stays blocked.",
}


def load_schema() -> dict:
    return json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))


def load_slice(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"❌ File not found: {path}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as exc:
        print(f"❌ Invalid JSON in {path}: {exc}", file=sys.stderr)
        sys.exit(1)


def save_slice(path: Path, data: dict) -> None:
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


# ---------------------------------------------------------------------------
# Sub-schema extraction
# ---------------------------------------------------------------------------

def resolve_ref(schema: dict, node: Any) -> Any:
    if not isinstance(node, dict) or "$ref" not in node:
        return node
    ref = node["$ref"]
    if ref.startswith("#/$defs/"):
        name = ref[len("#/$defs/"):]
        resolved = schema.get("$defs", {}).get(name)
        if resolved is not None:
            return resolved
    return node


def inline_refs(schema: dict, node: Any, depth: int = 0) -> Any:
    if depth > 20:
        return node
    if isinstance(node, dict):
        if "$ref" in node:
            resolved = resolve_ref(schema, node)
            return inline_refs(schema, resolved, depth + 1)
        return {k: inline_refs(schema, v, depth + 1) for k, v in node.items()}
    if isinstance(node, list):
        return [inline_refs(schema, item, depth + 1) for item in node]
    return node


def extract_subschema(schema: dict, section: str) -> dict:
    props = schema.get("properties", {})
    if section not in props:
        return {}
    node = props[section]
    return inline_refs(schema, node)


# ---------------------------------------------------------------------------
# ID assignment
# ---------------------------------------------------------------------------

def id_format(prefix: str, n: int) -> str:
    if prefix == "D":
        return f"D-{n:03d}"
    return f"{prefix}-{n:03d}"


def assign_ids(section: str, items: list, existing: list) -> list:
    prefix = ARRAY_SECTIONS[section]
    start = len(existing) + 1
    result = []
    for i, item in enumerate(items):
        new_id = id_format(prefix, start + i)
        if "id" in item:
            if item["id"] != new_id:
                print(
                    f"❌ ID mismatch: fragment item[{i}].id = {item['id']!r}, expected {new_id!r}",
                    file=sys.stderr,
                )
                sys.exit(1)
            result.append(item)
        else:
            result.append({"id": new_id, **item})
    return result


# ---------------------------------------------------------------------------
# Cross-reference validation
# ---------------------------------------------------------------------------

def collect_all_ids(data: dict) -> set[str]:
    ids: set[str] = set()
    for section in ARRAY_SECTIONS:
        for item in data.get(section, []):
            if isinstance(item, dict) and "id" in item:
                ids.add(item["id"])
    return ids


def check_cross_refs(data: dict, section: str, new_items: list) -> list[str]:
    errors: list[str] = []
    all_ids = collect_all_ids(data)

    if section == "acceptance_scenarios":
        br_ids = {item["id"] for item in data.get("business_rules", []) if "id" in item}
        traceable_ids = set()
        for s in ("business_rules", "decision_tables", "interfaces", "read_models",
                  "process_steps", "integration_contracts", "state_changes", "side_effects"):
            for item in data.get(s, []):
                if isinstance(item, dict) and "id" in item:
                    traceable_ids.add(item["id"])
        for i, item in enumerate(new_items):
            for trace_id in item.get("traces_to", []):
                if trace_id not in all_ids and trace_id not in traceable_ids:
                    errors.append(f"acceptance_scenarios[{i}].traces_to references unknown ID {trace_id!r}")

    if section == "decision_tables":
        br_ids = {item["id"] for item in data.get("business_rules", []) if "id" in item}
        for i, item in enumerate(new_items):
            for j, row in enumerate(item.get("rows", [])):
                for rule_id in row.get("rule_ids", []):
                    if rule_id not in br_ids:
                        errors.append(f"decision_tables[{i}].rows[{j}].rule_ids references unknown BR ID {rule_id!r}")

    if section == "decision_log":
        ev_ids = {item["id"] for item in data.get("codebase_evidence", []) if "id" in item}
        for i, item in enumerate(new_items):
            for ev_id in item.get("evidence_ids", []):
                if ev_id not in ev_ids:
                    errors.append(f"decision_log[{i}].evidence_ids references unknown EV ID {ev_id!r}")

    return errors


# ---------------------------------------------------------------------------
# Skeleton builders
# ---------------------------------------------------------------------------

def _base_skeleton(slice_type: str) -> dict:
    return {
        "schema_version": "1.1.0",
        "slice_type": slice_type,
        "risk": "medium",
        "depends_on": [],
        "title": "",
        "summary": "",
        "readiness": "blocked",
        "stakeholder_intent": {
            "actor": "",
            "goal": "",
            "business_value": "",
            "trigger": "",
            "success_outcome": "",
        },
        "scope": {"in_scope": [], "out_of_scope": []},
        "preconditions": [],
        "business_rules": [],
        "decision_tables": [],
        "interfaces": [],
        "read_models": [],
        "process_steps": [],
        "integration_contracts": [],
        "state_changes": [],
        "side_effects": [],
        "security": {
            "authentication": "",
            "authorization_rules": [],
            "data_classification": "",
            "audit_requirements": [],
        },
        "implementation_context": {
            "target_modules": [],
            "integration_points": [],
            "architecture_constraints": [],
            "allowed_changes": [],
            "forbidden_changes": [],
            "dependencies": [],
            "migration_strategy": "",
            "consistency_and_concurrency": "",
        },
        "acceptance_scenarios": [],
        "quality": {
            "required_test_levels": [],
            "test_cases": [],
            "quality_commands": [],
            "non_functional_requirements": [],
            "definition_of_done": [],
        },
        "decision_log": [],
        "codebase_evidence": [],
        "open_questions": [],
        "assumptions_requiring_confirmation": [],
        "blockers": [],
    }


# ---------------------------------------------------------------------------
# Subcommand: init
# ---------------------------------------------------------------------------

def cmd_init(args: argparse.Namespace) -> int:
    out = Path(args.out)
    if out.exists() and not args.force:
        print(f"❌ File already exists: {out}. Use --force to overwrite.", file=sys.stderr)
        return 1
    skeleton = _base_skeleton(args.slice_type)
    save_slice(out, skeleton)
    print(f"init: wrote {args.slice_type} skeleton to {out}")
    return 0


# ---------------------------------------------------------------------------
# Subcommand: plan
# ---------------------------------------------------------------------------

def _is_applicable(slice_type: str, section: str) -> bool:
    if section == "state_changes" and slice_type == "query":
        return False
    if section == "read_models" and slice_type not in ("query",):
        return True
    if section == "process_steps" and slice_type not in ("process",):
        return True
    if section == "integration_contracts" and slice_type not in ("integration",):
        return True
    return True


def _is_filled(data: dict, section: str) -> bool:
    val = data.get(section)
    if val is None:
        return False
    if isinstance(val, list):
        return len(val) > 0
    if isinstance(val, dict):
        return any(v not in ("", [], {}, None) for v in val.values())
    if isinstance(val, str):
        return val.strip() != ""
    return bool(val)


def cmd_plan(args: argparse.Namespace) -> int:
    path = Path(args.slice_json)
    data = load_slice(path)
    schema = load_schema()

    slice_type = data.get("slice_type", "?")
    readiness = data.get("readiness", "?")

    filled = sum(1 for s in PLAN_ORDER if _is_filled(data, s))
    total = len(PLAN_ORDER)
    print(f"slice_type: {slice_type}  readiness: {readiness}  progress: {filled}/{total} sections filled\n")

    for section in PLAN_ORDER:
        if not _is_applicable(slice_type, section):
            continue

        sub = extract_subschema(schema, section)
        compact_sub = json.dumps(sub, indent=2)

        existing_ids: list[str] = []
        if section in ARRAY_SECTIONS:
            existing_ids = [item.get("id", "") for item in data.get(section, []) if isinstance(item, dict)]

        status = "✅" if _is_filled(data, section) else "○"
        print(f"{status} {section}")
        print(f"```json")
        print(compact_sub)
        print(f"```")
        if existing_ids:
            print(f"   existing IDs: {', '.join(existing_ids)}")
        print(f"   → {PLAN_HINTS.get(section, 'Fill this section.')}")
        print()

    return 0


# ---------------------------------------------------------------------------
# Subcommand: set
# ---------------------------------------------------------------------------

def cmd_set(args: argparse.Namespace) -> int:
    path = Path(args.slice_json)
    data = load_slice(path)
    schema = load_schema()
    section = args.section
    slice_type = data.get("slice_type", "")
    if not _is_applicable(slice_type, section):
        print(
            f"❌ set {section}: not applicable for slice_type={slice_type} (schema conditional forbids it)",
            file=sys.stderr,
        )
        return 1

    try:
        fragment = json.loads(args.json_fragment)
    except json.JSONDecodeError as exc:
        print(f"❌ Invalid JSON fragment: {exc}", file=sys.stderr)
        return 1

    if section in ARRAY_SECTIONS:
        if not isinstance(fragment, list):
            print(f"❌ Section '{section}' expects a JSON array fragment.", file=sys.stderr)
            return 1
        for i, item in enumerate(fragment):
            if not isinstance(item, dict):
                print(
                    f"❌ {section}[{i}]: expected object, got {type(item).__name__}",
                    file=sys.stderr,
                )
                return 1
        existing = data.get(section, [])
        new_items = assign_ids(section, fragment, existing)

        cross_errors = check_cross_refs(data, section, new_items)
        if cross_errors:
            for e in cross_errors:
                print(f"❌ {e}", file=sys.stderr)
            return 1

        merged = existing + new_items
        test_data = dict(data)
        test_data[section] = merged

        sub = extract_subschema(schema, section)
        if sub.get("type") == "array":
            item_schema = sub.get("items", {})
            validator = Draft202012Validator(item_schema)
            for idx, item in enumerate(new_items):
                errs = list(validator.iter_errors(item))
                if errs:
                    for e in errs:
                        ptr = "/".join(str(p) for p in e.absolute_path)
                        loc = f"[{idx}]/{ptr}" if ptr else f"[{idx}]"
                        print(f"❌ {section}{loc}: {e.message}", file=sys.stderr)
                    return 1

            array_validator = Draft202012Validator(sub)
            array_errs = list(array_validator.iter_errors(merged))
            if array_errs:
                for e in array_errs:
                    print(f"❌ {section}: {e.message}", file=sys.stderr)
                return 1

        data[section] = merged
        start_id = id_format(ARRAY_SECTIONS[section], len(existing) + 1)
        end_id = id_format(ARRAY_SECTIONS[section], len(existing) + len(new_items))
        range_str = start_id if len(new_items) == 1 else f"{start_id}..{end_id}"
        print(f"set {section}: {len(new_items)} item(s) ({range_str})")

    elif section in OBJECT_SECTIONS:
        if not isinstance(fragment, dict):
            print(f"❌ Section '{section}' expects a JSON object fragment.", file=sys.stderr)
            return 1
        sub = extract_subschema(schema, section)
        validator = Draft202012Validator(sub)
        errs = list(validator.iter_errors(fragment))
        if errs:
            for e in errs:
                ptr = "/".join(str(p) for p in e.absolute_path)
                loc = f"/{ptr}" if ptr else ""
                print(f"❌ {section}{loc}: {e.message}", file=sys.stderr)
            return 1
        data[section] = fragment
        print(f"set {section}: object written")

    elif section in SCALAR_SECTIONS:
        sub = extract_subschema(schema, section)
        if sub:
            validator = Draft202012Validator(sub)
            errs = list(validator.iter_errors(fragment))
            if errs:
                for e in errs:
                    print(f"❌ {section}: {e.message}", file=sys.stderr)
                return 1
        data[section] = fragment
        print(f"set {section}: {json.dumps(fragment)}")

    else:
        print(f"❌ Unknown section: {section!r}. Valid sections: {sorted(list(ARRAY_SECTIONS) + list(OBJECT_SECTIONS) + list(SCALAR_SECTIONS))}", file=sys.stderr)
        return 1

    save_slice(path, data)
    return 0


# ---------------------------------------------------------------------------
# Subcommand: status
# ---------------------------------------------------------------------------

def cmd_status(args: argparse.Namespace) -> int:
    path = Path(args.slice_json)
    data = load_slice(path)

    slice_type = data.get("slice_type", "?")
    readiness = data.get("readiness", "?")
    print(f"slice_type: {slice_type}  readiness: {readiness}\n")

    for section in PLAN_ORDER:
        val = data.get(section)
        filled = _is_filled(data, section)
        status = "✅" if filled else "○"
        if isinstance(val, list):
            count = len(val)
            print(f"{status} {section}: {count} item(s)")
        elif isinstance(val, dict):
            filled_keys = [k for k, v in val.items() if v not in ("", [], {}, None)]
            print(f"{status} {section}: {len(filled_keys)}/{len(val)} keys filled")
        else:
            display = repr(val) if val is not None else "empty"
            print(f"{status} {section}: {display}")

    print()

    # Cross-reference gaps
    traceable_ids: set[str] = set()
    for s in ("business_rules", "decision_tables", "interfaces", "read_models",
              "process_steps", "integration_contracts", "state_changes", "side_effects"):
        for item in data.get(s, []):
            if isinstance(item, dict) and "id" in item:
                traceable_ids.add(item["id"])

    traced_ids: set[str] = set()
    for sc in data.get("acceptance_scenarios", []):
        for t in sc.get("traces_to", []):
            traced_ids.add(t)

    untraced = traceable_ids - traced_ids
    if untraced:
        print(f"⚠ Untraced items (not covered by any acceptance scenario): {', '.join(sorted(untraced))}")

    ev_ids = {item["id"] for item in data.get("codebase_evidence", []) if isinstance(item, dict) and "id" in item}
    referenced_ev: set[str] = set()
    for d in data.get("decision_log", []):
        for eid in d.get("evidence_ids", []):
            referenced_ev.add(eid)
    unreferenced_ev = ev_ids - referenced_ev
    if unreferenced_ev:
        print(f"⚠ Unreferenced evidence (not cited in any decision): {', '.join(sorted(unreferenced_ev))}")

    return 0


# ---------------------------------------------------------------------------
# Subcommand: finalize
# ---------------------------------------------------------------------------

def cmd_finalize(args: argparse.Namespace) -> int:
    path = Path(args.slice_json)
    data = load_slice(path)

    blocking_fields = ["open_questions", "assumptions_requiring_confirmation", "blockers"]
    has_blockers = any(data.get(f) for f in blocking_fields)

    current_readiness = data.get("readiness")
    violations: list[str] = []
    readiness_flipped = False

    if current_readiness == "ready" and has_blockers:
        for f in blocking_fields:
            if data.get(f):
                violations.append(f"{f} is non-empty")
        print(f"⚠ Warning: readiness was 'ready' but conditions violated: {'; '.join(violations)}", file=sys.stderr)
        print("⚠ Warning: Flipping readiness to 'blocked'.", file=sys.stderr)
        data["readiness"] = "blocked"
        readiness_flipped = True

    if has_blockers and data.get("readiness") != "blocked":
        data["readiness"] = "blocked"

    # Normalize: sort ID-bearing arrays by ID, strip trailing whitespace in strings
    def strip_strings(val: Any) -> Any:
        if isinstance(val, str):
            return val.rstrip()
        if isinstance(val, list):
            return [strip_strings(v) for v in val]
        if isinstance(val, dict):
            return {k: strip_strings(v) for k, v in val.items()}
        return val

    data = strip_strings(data)

    for section in ARRAY_SECTIONS:
        items = data.get(section, [])
        if items and isinstance(items[0], dict) and "id" in items[0]:
            data[section] = sorted(items, key=lambda x: x.get("id", ""))

    save_slice(path, data)

    # Delegate to validate_slice.py
    result = subprocess.run(
        [sys.executable, str(VALIDATE_SLICE), str(SCHEMA_PATH), str(path)],
        capture_output=True,
        text=True,
    )
    if result.stdout:
        print(result.stdout, end="")
    if result.stderr:
        print(result.stderr, end="", file=sys.stderr)

    if result.returncode != 0:
        return result.returncode

    if readiness_flipped:
        print(f"finalize: {path} normalized but readiness was auto-flipped to 'blocked'", file=sys.stderr)
        return 1

    print(f"finalize: {path} is valid and normalized")
    return 0


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(prog="slice.py", description="Grill-me slice driver")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_init = sub.add_parser("init", help="Write a minimal skeleton slice file")
    p_init.add_argument("slice_type", choices=["command", "query", "process", "integration"])
    p_init.add_argument("--out", default="slice.json")
    p_init.add_argument("--force", action="store_true")

    p_plan = sub.add_parser("plan", help="Print fill-order checklist")
    p_plan.add_argument("slice_json")

    p_set = sub.add_parser("set", help="Merge a section fragment into the slice")
    p_set.add_argument("slice_json")
    p_set.add_argument("section")
    p_set.add_argument("json_fragment")

    p_status = sub.add_parser("status", help="Show fill status and cross-reference gaps")
    p_status.add_argument("slice_json")

    p_finalize = sub.add_parser("finalize", help="Normalize, enforce readiness, validate")
    p_finalize.add_argument("slice_json")

    args = parser.parse_args()

    dispatch = {
        "init": cmd_init,
        "plan": cmd_plan,
        "set": cmd_set,
        "status": cmd_status,
        "finalize": cmd_finalize,
    }
    return dispatch[args.cmd](args)


if __name__ == "__main__":
    raise SystemExit(main())
