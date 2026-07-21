#!/usr/bin/env python3
"""Create a GitHub issue from a validated slice.json v2.

Usage:
    python3 create_issue.py <slice.json> [--blocked-by N[,N...]] [--dry-run]

Archives the JSON to .pi/skills/grill-me/.tmp/archive/<timestamp>_<issue-nr>.json on success.
Exit code 0 on success/dry-run, 1 on error.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path
from typing import Any, Optional

# Import validate_slice for full_validate
def _load_validate_module():
    validate_script = Path(__file__).parent / "validate_slice.py"
    spec = importlib.util.spec_from_file_location("_validate_slice", validate_script)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod

_validate_mod = _load_validate_module()

GITHUB_BODY_LIMIT = 65536
SCHEMA_PATH = Path(__file__).parent.parent / "schema.json"
DEFAULT_LABEL = "ready-for-agent"


def load_schema() -> dict:
    """Load schema."""
    try:
        return json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"❌ Schema not found: {SCHEMA_PATH}", file=sys.stderr)
        sys.exit(1)


def load_slice(path: Path) -> dict:
    """Load slice JSON."""
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"❌ File not found: {path}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as exc:
        print(f"❌ Invalid JSON in {path}: {exc}", file=sys.stderr)
        sys.exit(1)


def validate_full(data: dict, schema: dict = None) -> tuple[bool, list[str]]:
    """Full validation (structural + semantic). Returns (valid, errors)."""
    if schema is None:
        schema = load_schema()
    errors = _validate_mod.full_validate(data, schema)
    return len(errors) == 0, errors


def render_body(data: dict) -> str:
    """Render fixed Markdown body layout per v2 spec. All sections always present except conditional ones."""
    parts = []

    # TL;DR header block
    slice_type = data.get("slice_type", "?")
    change_type = data.get("change_type", "?")
    tldr = data.get("tldr", "")
    parts.append(f"> **TL;DR** · `{change_type}` · `{slice_type}` · {tldr}")
    parts.append(f"> **Trigger:** {data.get('trigger', '')}")
    parts.append(f"> **Result:** {data.get('success_outcome', '')}")

    scope_in = data.get("scope", {}).get("in", [])[:3]
    scope_out = data.get("scope", {}).get("out", [])[:3]
    in_bullets = "\n".join(f"  - {item}" for item in scope_in)
    out_bullets = "\n".join(f"  - {item}" for item in scope_out) if scope_out else "  - _None_"
    parts.append(f"> **In-Scope:**\n{in_bullets}")
    parts.append(f"> **Out-of-Scope:**\n{out_bullets}")

    # Stakeholder & Intent
    parts.append("## Stakeholder & Intent")
    parts.append(data.get("stakeholder", ""))

    # Scope
    parts.append("## Scope")
    parts.append("**In:**")
    for item in scope_in:
        parts.append(f"- {item}")
    parts.append("\n**Out:**")
    for item in scope_out:
        parts.append(f"- {item}")
    if not scope_out:
        parts.append("- _None_")

    # Behavior (always present)
    parts.append("## Behavior")
    behavior = data.get("behavior", "").strip()
    parts.append(behavior if behavior else "_None_")

    # Business Rules (always present, even if empty)
    parts.append("## Business Rules")
    business_rules = data.get("business_rules", "").strip()
    parts.append(business_rules if business_rules else "_None_")

    # Acceptance Scenarios (always present, even if empty)
    parts.append("## Acceptance Scenarios")
    acceptance = data.get("acceptance_scenarios", [])
    if acceptance:
        for scenario in acceptance:
            parts.append(f"- {scenario}")
    else:
        parts.append("_None_")

    # Proof of Delivery (always present)
    proof = data.get("proof", {})
    parts.append("## Proof of Delivery")
    parts.append("**How we show it works (plain language):**")
    how = proof.get("how", "").strip()
    parts.append(how if how else "_None_")
    parts.append("**Why this is convincing:**")
    why = proof.get("why", "").strip()
    parts.append(why if why else "_None_")
    checks = proof.get("checks", [])
    parts.append("**Reviewer checklist:**")
    if checks:
        table = [
            "| Functional (stakeholder) | Technical evidence (reviewer confirms) |",
            "| --- | --- |",
        ]
        for c in checks:
            functional = c.get("functional", "").replace("|", "\\|")
            technical = c.get("technical", "").replace("|", "\\|")
            table.append(f"| {functional} | {technical} |")
        parts.append("\n".join(table))
    else:
        parts.append("_None_")

    # Inputs / Outputs / Errors (always present)
    parts.append("## Inputs / Outputs / Errors")
    io_errors = data.get("inputs_outputs_errors", "").strip()
    parts.append(io_errors if io_errors else "_None_")

    # Codebase Evidence (always present, even if empty)
    parts.append("## Codebase Evidence")
    evidence = data.get("codebase_evidence", [])
    if evidence:
        for ev in evidence:
            location = ev.get("location", "?")
            note = ev.get("note", "")
            parts.append(f"- `{location}` — {note}")
    else:
        parts.append("_None_")

    # Security (conditional: only if security_relevant=true)
    if data.get("security_relevant"):
        parts.append("## Security")
        security = data.get("security", "").strip()
        parts.append(security if security else "_None_")

    # Verify (always present)
    parts.append("## Verify")
    verify = data.get("verify_commands", [])
    if verify:
        parts.append("```bash")
        for cmd in verify:
            parts.append(cmd)
        parts.append("```")
    else:
        parts.append("_None_")

    # Definition of Done (always present)
    parts.append("## Definition of Done")
    dod = data.get("definition_of_done", [])
    if dod:
        for item in dod:
            parts.append(f"- {item}")
    else:
        parts.append("_None_")

    # Blockers / Open Questions (conditional: only if non-empty)
    blockers = data.get("blockers", [])
    if blockers:
        parts.append("## Blockers / Open Questions")
        by_kind = {}
        for b in blockers:
            kind = b.get("kind", "blocker")
            if kind not in by_kind:
                by_kind[kind] = []
            by_kind[kind].append(b.get("text", ""))

        for kind in ["question", "assumption", "blocker"]:
            if kind in by_kind:
                label = {"question": "Questions", "assumption": "Assumptions", "blocker": "Blockers"}.get(kind, kind)
                parts.append(f"### {label}")
                for text in by_kind[kind]:
                    parts.append(f"- {text}")

    body = "\n\n".join(parts)
    return body


def archive_slice(slice_path: Path, issue_number: str) -> None:
    """Move JSON to .pi/skills/grill-me/.tmp/archive/<timestamp>_<issue-nr>.json"""
    archive_dir = Path(__file__).parent.parent / ".tmp" / "archive"
    archive_dir.mkdir(parents=True, exist_ok=True)

    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    archive_path = archive_dir / f"{timestamp}_{issue_number}.json"

    shutil.move(str(slice_path), str(archive_path))
    print(f"✓ Moved {slice_path} to {archive_path}", file=sys.stderr)


def run_gh(args: list[str], capture_output: bool = False) -> subprocess.CompletedProcess:
    """Run gh command."""
    return subprocess.run(
        ["gh"] + args,
        check=False,
        capture_output=capture_output,
        text=True,
    )


def create_issue(
    slice_path: Path,
    blocked_by: list[int],
    dry_run: bool,
    cwd: Path,
) -> int:
    """Main workflow."""
    schema = load_schema()
    data = load_slice(slice_path)

    # Validate (full: structural + semantic)
    valid, errors = validate_full(data, schema)
    if not valid:
        print("❌ Validation failed:", file=sys.stderr)
        for err in errors:
            print(f"  {err}", file=sys.stderr)
        return 1

    title = data.get("title", "Untitled")
    body = render_body(data)

    _CHANGE_TYPE_TO_ISSUE_TYPE = {"feature": "Feature", "bug": "Bug"}
    issue_type = _CHANGE_TYPE_TO_ISSUE_TYPE.get(data.get("change_type", ""), "Task")

    # Size check
    if len(body) > GITHUB_BODY_LIMIT:
        print(f"❌ Body too large ({len(body)} > {GITHUB_BODY_LIMIT} chars)", file=sys.stderr)
        return 1

    # Dry-run: print and exit
    if dry_run:
        print("=== Rendered Body ===\n")
        print(body)
        print("\n=== Planned Commands ===\n")
        print(f"gh issue create --title {title!r} --type {issue_type!r} --body <rendered-body>")
        for num in blocked_by:
            print(f"gh issue edit <N> --add-label ready-for-agent")
        return 0

    # Check auth
    result = run_gh(["auth", "status"], capture_output=True)
    if result.returncode != 0:
        print("❌ Not authenticated: run 'gh auth login'", file=sys.stderr)
        return 1

    # Create issue
    result = run_gh(
        ["issue", "create", "--title", title, "--type", issue_type, "--body", body],
        capture_output=True,
    )
    if result.returncode != 0:
        print(f"❌ Failed to create issue: {result.stderr}", file=sys.stderr)
        return 1

    issue_url = result.stdout.strip()
    issue_number = issue_url.rstrip("/").split("/")[-1]

    # Set blocked-by relations if specified
    for num in blocked_by:
        id_result = run_gh(
            ["api", f"/repos/{{owner}}/{{repo}}/issues/{num}", "-q", ".id"],
            capture_output=True,
        )
        if id_result.returncode == 0:
            dep_id = id_result.stdout.strip()
            block_result = run_gh(
                [
                    "api", "--method", "POST",
                    f"/repos/{{owner}}/{{repo}}/issues/{issue_number}/dependencies/blocked_by",
                    "-F", f"issue_id={dep_id}",
                ],
                capture_output=True,
            )
            if block_result.returncode != 0:
                print(f"⚠ Could not set blocked-by {num}: {block_result.stderr}", file=sys.stderr)

    # Add label
    label_result = run_gh(
        ["issue", "edit", issue_number, "--add-label", DEFAULT_LABEL],
        capture_output=True,
    )
    if label_result.returncode != 0:
        print(f"⚠ Could not add label: {label_result.stderr}", file=sys.stderr)

    # Archive JSON (moves file, removing the source)
    archive_slice(slice_path, issue_number)

    print(f"✓ Created {issue_url}")
    return 0


def main() -> int:
    """CLI entry point."""
    parser = argparse.ArgumentParser(
        description="Create a GitHub issue from validated slice.json v2"
    )
    parser.add_argument("slice_json", help="Path to slice.json")
    parser.add_argument(
        "--blocked-by",
        help="Comma-separated issue numbers to block on",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print rendered body without creating issue",
    )
    args = parser.parse_args()

    cwd = Path(os.environ.get("PWD", os.getcwd()))
    slice_path = Path(args.slice_json)

    blocked_by = []
    if args.blocked_by:
        try:
            blocked_by = [int(n.strip()) for n in args.blocked_by.split(",") if n.strip()]
        except ValueError as exc:
            print(f"❌ Invalid --blocked-by: {exc}", file=sys.stderr)
            return 1

    return create_issue(
        slice_path=slice_path,
        blocked_by=blocked_by,
        dry_run=args.dry_run,
        cwd=cwd,
    )


if __name__ == "__main__":
    raise SystemExit(main())
