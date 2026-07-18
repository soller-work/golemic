#!/usr/bin/env python3
"""Create a GitHub issue from a validated implementation-slice JSON.

Usage:
    python3 create_issue.py <slice.json> [--blocked-by N[,N...]] [--dry-run]

Exit codes:
    0  success or dry-run
    1  any error (error code printed to stderr)
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Callable, List, Optional, Protocol

GITHUB_BODY_LIMIT = 65536
SCHEMA_PATH = Path(__file__).parent.parent / "schema.json"
DEFAULT_LABEL = "ready-for-agent"
SLICE_COMMENT_MARKER = "<!-- golemic:slice-json v=1 -->"

RISK_LABEL_COLORS = {
    "low": "0e8a16",
    "medium": "e4e669",
    "high": "d93f0b",
}


# ---------------------------------------------------------------------------
# Injectable gh runner interface
# ---------------------------------------------------------------------------

class GhRunner(Protocol):
    def run(
        self,
        args: List[str],
        *,
        check: bool = True,
        capture_output: bool = False,
    ) -> subprocess.CompletedProcess: ...


class RealGhRunner:
    def run(
        self,
        args: List[str],
        *,
        check: bool = True,
        capture_output: bool = False,
    ) -> subprocess.CompletedProcess:
        return subprocess.run(
            ["gh"] + args,
            check=check,
            capture_output=capture_output,
            text=True,
        )


# ---------------------------------------------------------------------------
# Validation (reuses validate_slice.py)
# ---------------------------------------------------------------------------

def _load_validate_module():
    validate_script = Path(__file__).parent / "validate_slice.py"
    spec = importlib.util.spec_from_file_location("_validate_slice_mod", validate_script)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def validate_slice_file(schema_path: Path, slice_path: Path) -> List[str]:
    """Validate slice against schema. Returns list of error messages; empty means valid."""
    try:
        from jsonschema import Draft202012Validator
    except ImportError as exc:
        raise SystemExit(
            "Missing dependency 'jsonschema'. Install with: pip install jsonschema"
        ) from exc

    mod = _load_validate_module()
    schema = mod.load_json(schema_path)
    document = mod.load_json(slice_path)
    validator = Draft202012Validator(schema)
    structural = sorted(validator.iter_errors(document), key=lambda e: list(e.absolute_path))
    semantic = mod.semantic_errors(document) if not structural else []
    errors = [f"{mod.json_path(e.absolute_path)}: {e.message}" for e in structural]
    errors.extend(semantic)
    return errors


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

def _load_config(cwd: Path) -> dict:
    config_path = cwd / ".golemic" / "config.json"
    if config_path.exists():
        try:
            return json.loads(config_path.read_text(encoding="utf-8"))
        except Exception:
            pass
    return {}


def get_label_name(config: dict) -> str:
    return config.get("label") or DEFAULT_LABEL


# ---------------------------------------------------------------------------
# Deterministic Markdown renderer (BR-001, BR-002, D-008)
# ---------------------------------------------------------------------------

def render_body(slice_data: dict) -> str:
    """Render a compact Markdown issue body per BR-002.

    Contains only: Type/Risk header, Summary, Stakeholder Intent (actor/goal/success_outcome),
    Scope, and a footer with the slice-comment marker so the runner can locate the JSON.
    Identical input always yields byte-identical output.
    """
    parts: List[str] = []

    slice_type = slice_data.get("slice_type", "")
    risk = slice_data.get("risk", "")
    parts.append(f"**Type:** {slice_type} | **Risk:** {risk}")

    parts.append(f"## Summary\n\n{slice_data['summary']}")

    si = slice_data.get("stakeholder_intent", {})
    parts.append(
        "## Stakeholder Intent\n\n"
        f"**Actor:** {si.get('actor', '')}\n\n"
        f"**Goal:** {si.get('goal', '')}\n\n"
        f"**Success Outcome:** {si.get('success_outcome', '')}"
    )

    scope = slice_data.get("scope", {})
    in_items = "\n".join(f"- {x}" for x in scope.get("in_scope", []))
    out_items = (
        "\n".join(f"- {x}" for x in scope.get("out_of_scope", []))
        or "_None_"
    )
    parts.append(
        f"## Scope\n\n**In Scope:**\n\n{in_items}\n\n**Out of Scope:**\n\n{out_items}"
    )

    body = "\n\n".join(parts)
    body += (
        "\n\n---\n\n"
        f"{SLICE_COMMENT_MARKER}\n"
        "_The canonical machine-readable slice JSON is posted as the first bot comment on this issue._"
    )
    return body


def render_slice_comment(canonical_json: str) -> str:
    """Render the slice comment body per BR-001.

    Starts with the exact marker on line 1, blank line, then a single ```json``` block.
    """
    return f"{SLICE_COMMENT_MARKER}\n\n```json\n{canonical_json}\n```"


# ---------------------------------------------------------------------------
# Preconditions
# ---------------------------------------------------------------------------

def _check_preconditions(gh: GhRunner, cwd: Path) -> List[str]:
    errors: List[str] = []

    result = gh.run(["auth", "status"], check=False, capture_output=True)
    if result.returncode != 0:
        errors.append("gh auth status failed — not authenticated")

    git_result = subprocess.run(
        ["git", "remote", "get-url", "origin"],
        check=False,
        capture_output=True,
        text=True,
        cwd=str(cwd),
    )
    if git_result.returncode != 0:
        errors.append("Not a git repository or no 'origin' remote configured")
    elif "github.com" not in git_result.stdout:
        errors.append(
            f"Origin remote does not point to GitHub: {git_result.stdout.strip()!r}"
        )

    return errors


# ---------------------------------------------------------------------------
# Label helpers (SE-001)
# ---------------------------------------------------------------------------

def _ensure_label(gh: GhRunner, label_name: str, color: str = "0075ca", description: str = "Ready for autonomous agent") -> None:
    result = gh.run(
        [
            "label", "create", label_name,
            "--color", color,
            "--description", description,
        ],
        check=False,
        capture_output=True,
    )
    if result.returncode != 0 and "already exists" not in result.stderr.lower():
        raise subprocess.CalledProcessError(
            result.returncode,
            ["gh", "label", "create", label_name],
            stderr=result.stderr,
        )


def _ensure_risk_label(gh: GhRunner, risk_value: str) -> None:
    color = RISK_LABEL_COLORS[risk_value]
    _ensure_label(
        gh,
        f"risk:{risk_value}",
        color=color,
        description=f"Risk level: {risk_value}",
    )


# ---------------------------------------------------------------------------
# Dry-run command listing
# ---------------------------------------------------------------------------

def _planned_commands(title: str, blocked_by: List[int], label_name: str, risk_value: str) -> List[str]:
    cmds = [f"gh issue create --title {title!r} --body <compact-body>"]
    cmds.append(
        "gh api --method POST repos/:owner/:repo/issues/<N>/comments"
        " -f body=<slice-comment-body>  # BR-001: post canonical JSON as first bot comment"
    )
    cmds.append(
        "# if above fails: gh issue close <N> --comment 'slice-comment post failed: <stderr>'"
        "  # BR-003: compensation — no labels attached"
    )
    for n in blocked_by:
        cmds.append(
            f"gh api /repos/{{owner}}/{{repo}}/issues/{n} -q .id  (resolve internal id)"
        )
        cmds.append(
            f"gh api --method POST "
            f"/repos/{{owner}}/{{repo}}/issues/<N>/dependencies/blocked_by"
            f" -F issue_id=<id-of-{n}>"
        )
    risk_label = f"risk:{risk_value}"
    risk_color = RISK_LABEL_COLORS[risk_value]
    cmds.append(
        f"gh label create {risk_label!r} --color {risk_color}"
        f" --description 'Risk level: {risk_value}'  (idempotent)"
    )
    cmds.append(
        f"gh label create {label_name!r} --color 0075ca"
        f" --description 'Ready for autonomous agent'  (idempotent)"
    )
    cmds.append(f"gh issue edit <N> --add-label {label_name!r},{risk_label!r}")
    return cmds


# ---------------------------------------------------------------------------
# Core logic (injectable for tests)
# ---------------------------------------------------------------------------

def run(
    slice_path: Path,
    blocked_by: List[int],
    dry_run: bool,
    gh: GhRunner,
    cwd: Path,
    schema_path: Path = SCHEMA_PATH,
    validate_fn: Optional[Callable[[Path], List[str]]] = None,
) -> int:
    """Execute the create-issue workflow. Separated for testability."""
    if validate_fn is None:
        validate_fn = lambda p: validate_slice_file(schema_path, p)  # noqa: E731

    # BR-009: re-validate on every invocation including --dry-run
    errors = validate_fn(slice_path)
    if errors:
        print("VALIDATION_FAILED:", file=sys.stderr)
        for err in errors:
            print(f"  - {err}", file=sys.stderr)
        return 1

    raw = json.loads(slice_path.read_text(encoding="utf-8"))
    canonical_json = json.dumps(raw, indent=2, ensure_ascii=False)
    body = render_body(raw)
    slice_comment = render_slice_comment(canonical_json)

    # BR-007: fail-closed size check for both artifacts before any write
    if len(body) > GITHUB_BODY_LIMIT:
        print(
            f"BODY_TOO_LARGE: Rendered body is {len(body)} chars; "
            f"limit is {GITHUB_BODY_LIMIT}",
            file=sys.stderr,
        )
        return 1
    if len(slice_comment) > GITHUB_BODY_LIMIT:
        print(
            f"BODY_TOO_LARGE: Rendered slice comment is {len(slice_comment)} chars; "
            f"limit is {GITHUB_BODY_LIMIT}",
            file=sys.stderr,
        )
        return 1

    title = raw["title"]
    risk_value = raw["risk"]
    config = _load_config(cwd)
    label_name = get_label_name(config)

    # --dry-run: print and exit without any write (AC-002)
    if dry_run:
        print("=== Rendered Body ===\n")
        print(body)
        print("\n=== Rendered Slice Comment ===\n")
        print(slice_comment)
        print("\n=== Planned gh Commands ===\n")
        for cmd in _planned_commands(title, blocked_by, label_name, risk_value):
            print(cmd)
        return 0

    # Preconditions (checked after dry-run branch so dry-run works offline)
    pre_errors = _check_preconditions(gh, cwd)
    if pre_errors:
        for err in pre_errors:
            print(f"PRECONDITION_FAILED: {err}", file=sys.stderr)
        return 1

    # Step 1: create issue (SC-001, BR-003)
    try:
        result = gh.run(
            ["issue", "create", "--title", title, "--body", body],
            capture_output=True,
        )
    except subprocess.CalledProcessError as exc:
        print(f"CREATE_FAILED: {exc.stderr}", file=sys.stderr)
        return 1

    issue_url = result.stdout.strip()
    issue_number = issue_url.rstrip("/").split("/")[-1]

    # Step 2: post slice comment (SC-002, BR-001, BR-003, BR-004)
    try:
        gh.run(
            [
                "api", "--method", "POST",
                f"repos/:owner/:repo/issues/{issue_number}/comments",
                "-f", f"body={slice_comment}",
            ],
            capture_output=True,
        )
    except subprocess.CalledProcessError as exc:
        stderr_text = exc.stderr or ""
        try:
            gh.run(
                ["issue", "close", issue_number,
                 "--comment", f"slice-comment post failed: {stderr_text}"],
            )
        except subprocess.CalledProcessError:
            pass
        print(f"SLICE_COMMENT_FAILED: {stderr_text}", file=sys.stderr)
        return 1

    # Step 3: blocked_by relations (SC-003-after, BR-004) — one gh api call per number.
    # The dependencies API keys on the internal issue id, not the issue number,
    # so each dependency number is resolved to its id first.
    for i, dep_num in enumerate(blocked_by):
        try:
            id_result = gh.run(
                [
                    "api", f"/repos/{{owner}}/{{repo}}/issues/{dep_num}",
                    "-q", ".id",
                ],
                capture_output=True,
            )
            dep_id = id_result.stdout.strip()
            gh.run(
                [
                    "api", "--method", "POST",
                    f"/repos/{{owner}}/{{repo}}/issues/{issue_number}/dependencies/blocked_by",
                    "-F", f"issue_id={dep_id}",
                ],
            )
        except subprocess.CalledProcessError:
            remaining = [f"blocked-by {n}" for n in blocked_by[i:]]
            print(
                f"PARTIAL_FAILURE: Issue #{issue_number} created at {issue_url}",
                file=sys.stderr,
            )
            print(
                f"Unfinished steps: {', '.join(remaining)}; "
                f"label '{label_name}' must be attached manually",
                file=sys.stderr,
            )
            return 1

    # Step 4: labels — LAST so a partially created issue is never takeable (BR-004, SC-004)
    risk_label = f"risk:{risk_value}"
    try:
        _ensure_risk_label(gh, risk_value)
        _ensure_label(gh, label_name)
        gh.run(["issue", "edit", issue_number, "--add-label", f"{label_name},{risk_label}"])
    except subprocess.CalledProcessError:
        print(
            f"PARTIAL_FAILURE: Issue #{issue_number} created at {issue_url}",
            file=sys.stderr,
        )
        print(
            f"Unfinished steps: labels '{risk_label}' and '{label_name}' must be attached manually",
            file=sys.stderr,
        )
        return 1

    print(f"#{issue_number} {issue_url}")
    return 0


# ---------------------------------------------------------------------------
# CLI entry point
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(
        description="Create a GitHub issue from a validated implementation-slice JSON."
    )
    parser.add_argument("slice_json", help="Path to the slice JSON file")
    parser.add_argument(
        "--blocked-by",
        help="Comma-separated GitHub issue numbers this issue is blocked by",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print rendered body and planned gh commands; execute nothing",
    )
    args = parser.parse_args()

    cwd = Path(os.environ.get("PWD", os.getcwd()))
    slice_path = Path(args.slice_json)

    if not slice_path.exists():
        print(f"VALIDATION_FAILED: File not found: {slice_path}", file=sys.stderr)
        return 1

    blocked_by: List[int] = []
    if args.blocked_by:
        try:
            parsed = [
                int(n.strip()) for n in args.blocked_by.split(",") if n.strip()
            ]
        except ValueError as exc:
            print(
                f"VALIDATION_FAILED: --blocked-by must be comma-separated integers: {exc}",
                file=sys.stderr,
            )
            return 1
        non_positive = [n for n in parsed if n <= 0]
        if non_positive:
            print(
                f"VALIDATION_FAILED: --blocked-by values must be positive integers, got: {non_positive}",
                file=sys.stderr,
            )
            return 1
        blocked_by = parsed

    return run(
        slice_path=slice_path,
        blocked_by=blocked_by,
        dry_run=args.dry_run,
        gh=RealGhRunner(),
        cwd=cwd,
    )


if __name__ == "__main__":
    raise SystemExit(main())
