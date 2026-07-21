#!/usr/bin/env python3
"""Slice workflow CLI for grill-me v2. Subcommands: new, write, set, check, plan, finalize."""

from __future__ import annotations

import argparse
import importlib.util
import json
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

# Load validate_slice module for full_validate
def _load_validate_module():
    spec = importlib.util.spec_from_file_location("_validate_slice", VALIDATE_SLICE)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

_validate_mod = _load_validate_module()


def _load_detail_blocks():
    mod_path = Path(__file__).parent / "detail_blocks.py"
    spec = importlib.util.spec_from_file_location("_detail_blocks", mod_path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

_detail_blocks = _load_detail_blocks()

# Plan order / hints. The gattung-specific detail fields (spliced after 'scope',
# before 'proof') come from the registry, so the field-to-gattung map lives in
# exactly one place (detail_blocks.py).
_CORE_HINTS = {
    "change_type": "Enum: 'feature', 'bug', or 'refactoring' — intent/proof category; detail fields depend on this.",
    "stakeholder": "String: who is the primary stakeholder.",
    "trigger": "String: what triggers this capability.",
    "success_outcome": "String: the desired outcome.",
    "tldr": "String (≤140 chars): concise summary.",
    "scope": "Object: {in: [...], out: [...]} — at least 1 in-scope item.",
    "proof": "Object: {how, why, checks:[{functional, technical}]} — plain-language proof-of-delivery plan; functional=stakeholder ticks off, technical=implementation-agnostic criterion the reviewer confirms.",
    "codebase_evidence": "Array of {location: 'path:line', note: '...'} — findings from repo.",
    "verify_commands": "Array of strings: test, lint, deploy commands.",
    "definition_of_done": "Array of strings: completion criteria.",
    "security_relevant": "Boolean: set true if security implications exist.",
    "security": "String (Markdown, required if security_relevant=true).",
    "blockers": "Array of {kind: 'question'|'assumption'|'blocker', text: '...'} — empty when ready.",
    "readiness": "Enum: 'ready' or 'blocked' (set by finalize).",
}


def plan_order(change_type: str) -> list[str]:
    """Fill order with the detail fields for change_type spliced after 'scope'."""
    return [
        "change_type", "stakeholder", "trigger", "success_outcome", "tldr", "scope",
        *[f.key for f in _detail_blocks.detail_fields(change_type)],
        "proof", "codebase_evidence", "verify_commands", "definition_of_done",
        "security_relevant", "security", "blockers", "readiness",
    ]


def plan_hints(change_type: str) -> dict[str, str]:
    hints = dict(_CORE_HINTS)
    for f in _detail_blocks.detail_fields(change_type):
        hints[f.key] = f.hint
    return hints


def load_schema() -> dict:
    """Load schema from disk."""
    try:
        return json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"❌ Schema not found: {SCHEMA_PATH}", file=sys.stderr)
        sys.exit(1)


def load_slice(path: Path) -> dict:
    """Load slice JSON from disk."""
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"❌ File not found: {path}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as exc:
        print(f"❌ Invalid JSON in {path}: {exc}", file=sys.stderr)
        sys.exit(1)


def save_slice(path: Path, data: dict) -> None:
    """Save slice JSON to disk."""
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def validate_full(data: dict, schema: dict = None) -> tuple[bool, list[str]]:
    """Full validation (structural + semantic). Returns (valid, errors)."""
    if schema is None:
        schema = load_schema()
    errors = _validate_mod.full_validate(data, schema)
    return len(errors) == 0, errors


def cmd_new(args: argparse.Namespace) -> None:
    """Create a skeleton slice seeded with the detail fields for its change_type."""
    slice_type = args.type.lower()
    if slice_type not in ["command", "query", "process", "integration"]:
        print(f"❌ Unknown slice_type: {slice_type}. Must be: command|query|process|integration", file=sys.stderr)
        sys.exit(1)

    change_type = args.change_type.lower()
    if change_type not in _detail_blocks.DETAIL_BLOCKS:
        print(f"❌ Unknown change_type: {change_type}. Must be: feature|bug|refactoring", file=sys.stderr)
        sys.exit(1)

    path = Path(args.file) if args.file else Path(".pi/skills/grill-me/.tmp/slice.json")

    skeleton = {
        "slice_type": slice_type,
        "change_type": change_type,
        "title": "",
        "stakeholder": "",
        "trigger": "",
        "success_outcome": "",
        "tldr": "",
        "scope": {"in": [], "out": []},
    }
    for field in _detail_blocks.detail_fields(change_type):
        skeleton[field.key] = [] if field.kind == "scenarios" else ""
    skeleton.update({
        "proof": {"how": "", "why": "", "checks": []},
        "codebase_evidence": [],
        "verify_commands": [],
        "definition_of_done": [],
        "security_relevant": False,
        "blockers": [],
        "readiness": "blocked",
    })

    save_slice(path, skeleton)
    print(f"✓ Created skeleton at {path}")
    print(f"  Next: slice.py write {path} '<json>' or slice.py set {path} <section> '<fragment>'")


def cmd_write(args: argparse.Namespace) -> None:
    """Bulk write: load full JSON in one call, validate atomically."""
    path = Path(args.path)
    
    # Parse JSON from stdin or CLI arg
    if args.json.startswith("@"):
        json_path = Path(args.json[1:])
        try:
            json_text = json_path.read_text(encoding="utf-8")
        except FileNotFoundError:
            print(f"❌ File not found: {json_path}", file=sys.stderr)
            sys.exit(1)
    else:
        json_text = args.json

    try:
        data = json.loads(json_text)
    except json.JSONDecodeError as exc:
        print(f"❌ Invalid JSON: {exc}", file=sys.stderr)
        sys.exit(1)

    schema = load_schema()
    valid, errors = validate_full(data, schema)
    if not valid:
        print("❌ Validation failed:", file=sys.stderr)
        for err in errors:
            print(f"  {err}", file=sys.stderr)
        sys.exit(1)

    save_slice(path, data)
    print(f"✓ Written and validated {path}")


def cmd_set(args: argparse.Namespace) -> None:
    """Set a single section with a JSON fragment, validate the whole."""
    path = Path(args.path)
    section = args.section
    fragment_text = args.fragment

    slice_data = load_slice(path)
    schema = load_schema()

    # Parse fragment
    try:
        fragment = json.loads(fragment_text)
    except json.JSONDecodeError as exc:
        print(f"❌ Invalid JSON fragment: {exc}", file=sys.stderr)
        sys.exit(1)

    # Set and validate
    slice_data[section] = fragment
    valid, errors = validate_full(slice_data, schema)
    if not valid:
        print(f"❌ Validation failed after setting {section}:", file=sys.stderr)
        for err in errors:
            print(f"  {err}", file=sys.stderr)
        sys.exit(1)

    save_slice(path, slice_data)
    print(f"✓ Set {section}")


def cmd_check(args: argparse.Namespace) -> None:
    """Read-only validation: check without modifying."""
    path = Path(args.path)
    slice_data = load_slice(path)
    schema = load_schema()

    valid, errors = validate_full(slice_data, schema)
    if valid:
        print(f"✓ {path} is valid")
        sys.exit(0)
    else:
        print(f"❌ {path} has validation errors:", file=sys.stderr)
        for err in errors:
            print(f"  {err}", file=sys.stderr)
        sys.exit(1)


def cmd_finalize(args: argparse.Namespace) -> None:
    """Normalize readiness first, then validate, then save."""
    path = Path(args.path)
    slice_data = load_slice(path)
    schema = load_schema()

    # Step 1: Normalize readiness BEFORE validation
    blockers = slice_data.get("blockers", [])
    if args.blocked:
        # User explicitly requested blocked
        if len(blockers) == 0:
            # Error: can't be blocked without blockers
            print(
                "❌ --blocked was requested but no blockers are set. "
                "Add one first with: slice.py set <path> blockers "
                "'[{\"kind\":\"blocker\",\"text\":\"...\"}]'",
                file=sys.stderr,
            )
            sys.exit(1)
        slice_data["readiness"] = "blocked"
    elif len(blockers) == 0:
        # Auto-ready: no blockers means ready
        slice_data["readiness"] = "ready"
    else:
        # Auto-blocked: blockers present means blocked
        slice_data["readiness"] = "blocked"

    # Step 2: Validate the normalized document
    valid, errors = validate_full(slice_data, schema)
    if not valid:
        print(f"❌ Validation failed:", file=sys.stderr)
        for err in errors:
            print(f"  {err}", file=sys.stderr)
        sys.exit(1)

    # Step 3: Save only if validation passes
    save_slice(path, slice_data)
    print(f"✓ Finalized: readiness={slice_data['readiness']}")


def cmd_plan(args: argparse.Namespace) -> None:
    """Show section names in fill order with one-line hints. --verbose shows sub-schemas.

    Detail fields follow the slice's change_type: taken from --change-type if given,
    else read from the slice at the provided path, else defaulting to 'feature'.
    """
    schema = load_schema()

    change_type = args.change_type
    if change_type is None and args.path:
        candidate = Path(args.path)
        if candidate.exists():
            change_type = load_slice(candidate).get("change_type")
    if change_type not in _detail_blocks.DETAIL_BLOCKS:
        change_type = "feature"

    order = plan_order(change_type)
    hints = plan_hints(change_type)

    if args.verbose:
        # Dump section sub-schemas
        props = schema.get("properties", {})
        for section in order:
            if section in props:
                print(f"\n## {section}")
                print(json.dumps(props[section], indent=2))
    else:
        print(f"Fill order (change_type={change_type}):")
        for i, section in enumerate(order, 1):
            hint = hints.get(section, "")
            print(f"{i:2d}. {section:30s} — {hint}")


def main() -> None:
    """Main CLI entry point."""
    parser = argparse.ArgumentParser(
        description="Slice workflow for grill-me v2 (ephemeral JSON, full semantic validation).",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    # new <type> [--file PATH]
    new_parser = subparsers.add_parser("new", help="Create skeleton (no N/A sections for type)")
    new_parser.add_argument("type", help="Slice type: command|query|process|integration")
    new_parser.add_argument("--change-type", dest="change_type", default="feature", help="Change type: feature|bug|refactoring (default: feature)")
    new_parser.add_argument("--file", help="Output path (default: .pi/skills/grill-me/.tmp/slice.json)")
    new_parser.set_defaults(func=cmd_new)

    # write <path> <json>
    write_parser = subparsers.add_parser("write", help="Bulk write: full JSON in one call, atomic validation")
    write_parser.add_argument("path", help="Path to slice.json")
    write_parser.add_argument("json", help="JSON string or @file")
    write_parser.set_defaults(func=cmd_write)

    # set <path> <section> <fragment>
    set_parser = subparsers.add_parser("set", help="Set one section with JSON fragment")
    set_parser.add_argument("path", help="Path to slice.json")
    set_parser.add_argument("section", help="Section name (e.g., 'business_rules')")
    set_parser.add_argument("fragment", help="JSON fragment")
    set_parser.set_defaults(func=cmd_set)

    # check <path>
    check_parser = subparsers.add_parser("check", help="Read-only validation")
    check_parser.add_argument("path", help="Path to slice.json")
    check_parser.set_defaults(func=cmd_check)

    # finalize <path> [--blocked]
    finalize_parser = subparsers.add_parser("finalize", help="Validate and set readiness")
    finalize_parser.add_argument("path", help="Path to slice.json")
    finalize_parser.add_argument("--blocked", action="store_true", help="Force readiness=blocked")
    finalize_parser.set_defaults(func=cmd_finalize)

    # plan <path> [--verbose]
    plan_parser = subparsers.add_parser("plan", help="Show fill order and hints")
    plan_parser.add_argument("path", nargs="?", help="Slice path; its change_type selects the detail fields")
    plan_parser.add_argument("--change-type", dest="change_type", default=None, help="Override change_type: feature|bug|refactoring")
    plan_parser.add_argument("--verbose", action="store_true", help="Show sub-schemas")
    plan_parser.set_defaults(func=cmd_plan)

    args = parser.parse_args()
    if hasattr(args, "func"):
        args.func(args)
    else:
        parser.print_help()


if __name__ == "__main__":
    main()
