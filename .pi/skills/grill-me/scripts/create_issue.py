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
DETAILS_MARKER = "Implementation Slice (machine-readable)"


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

def render_body(slice_data: dict, canonical_json: str) -> str:
    """Render a deterministic Markdown body from slice data.

    Identical input always yields byte-identical output.
    The embedded JSON block is the authoritative specification (BR-002).
    """
    parts: List[str] = []

    parts.append(f"## Summary\n\n{slice_data['summary']}")

    si = slice_data.get("stakeholder_intent", {})
    parts.append(
        "## Stakeholder Intent\n\n"
        f"**Actor:** {si.get('actor', '')}\n\n"
        f"**Goal:** {si.get('goal', '')}\n\n"
        f"**Business Value:** {si.get('business_value', '')}\n\n"
        f"**Trigger:** {si.get('trigger', '')}\n\n"
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

    pre_items = "\n".join(f"- {p}" for p in slice_data.get("preconditions", []))
    parts.append(f"## Preconditions\n\n{pre_items}")

    rule_blocks: List[str] = []
    for r in slice_data.get("business_rules", []):
        rule_blocks.append(
            f"**{r['id']}:** {r['rule']}\n\n"
            f"_Applies when:_ {r['applies_when']}\n\n"
            f"_Outcome:_ {r['outcome']}"
        )
    if rule_blocks:
        parts.append("## Business Rules\n\n" + "\n\n---\n\n".join(rule_blocks))

    iface_blocks: List[str] = []
    for iface in slice_data.get("interfaces", []):
        text = (
            f"**{iface['id']} \u2014 {iface['name']}** (`{iface['kind']}`)\n\n"
            f"Operation: `{iface['operation']}`"
        )
        inputs = iface.get("inputs", [])
        if inputs:
            input_lines = "\n".join(
                "- `{name}` ({type}, {req}){constraints}".format(
                    name=inp["name"],
                    type=inp["type"],
                    req="required" if inp["required"] else "optional",
                    constraints=(
                        ": " + "; ".join(inp["constraints"])
                        if inp.get("constraints")
                        else ""
                    ),
                )
                for inp in inputs
            )
            text += f"\n\n**Inputs:**\n\n{input_lines}"
        errors = iface.get("errors", [])
        if errors:
            error_lines = "\n".join(
                f"- `{e['code']}`: {e['message']}" for e in errors
            )
            text += f"\n\n**Errors:**\n\n{error_lines}"
        iface_blocks.append(text)
    if iface_blocks:
        parts.append("## Interfaces\n\n" + "\n\n---\n\n".join(iface_blocks))

    scenario_blocks: List[str] = []
    for sc in slice_data.get("acceptance_scenarios", []):
        given = "\n".join(f"- {g}" for g in sc.get("given", []))
        when = "\n".join(f"- {w}" for w in sc.get("when", []))
        then = "\n".join(f"- {t}" for t in sc.get("then", []))
        scenario_blocks.append(
            f"**{sc['id']} \u2014 {sc['title']}**\n\n"
            f"_Given:_\n{given}\n\n"
            f"_When:_\n{when}\n\n"
            f"_Then:_\n{then}"
        )
    if scenario_blocks:
        parts.append(
            "## Acceptance Scenarios\n\n" + "\n\n---\n\n".join(scenario_blocks)
        )

    quality = slice_data.get("quality", {})
    dod_items = "\n".join(f"- {d}" for d in quality.get("definition_of_done", []))
    qcmd_items = "\n".join(f"- `{c}`" for c in quality.get("quality_commands", []))
    parts.append(
        f"## Definition of Done\n\n{dod_items}\n\n**Quality Commands:**\n\n{qcmd_items}"
    )

    body = "\n\n".join(parts)

    body += (
        "\n\n---\n\n"
        "> The embedded JSON block below is the authoritative machine-readable "
        "specification. The Markdown sections above are its human-readable "
        "projection. In any conflict, the JSON is correct.\n\n"
        f"<details>\n<summary>{DETAILS_MARKER}</summary>\n\n"
        f"```json\n{canonical_json}\n```\n\n"
        "</details>"
    )

    return body


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

def _ensure_label(gh: GhRunner, label_name: str) -> None:
    result = gh.run(
        [
            "label", "create", label_name,
            "--color", "0075ca",
            "--description", "Ready for autonomous agent",
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


# ---------------------------------------------------------------------------
# Dry-run command listing
# ---------------------------------------------------------------------------

def _planned_commands(title: str, blocked_by: List[int], label_name: str) -> List[str]:
    cmds = [f"gh issue create --title {title!r} --body <rendered-body>"]
    for n in blocked_by:
        cmds.append(
            f"gh api /repos/{{owner}}/{{repo}}/issues/{n} -q .id  (resolve internal id)"
        )
        cmds.append(
            f"gh api --method POST "
            f"/repos/{{owner}}/{{repo}}/issues/<new-number>/dependencies/blocked_by"
            f" -F issue_id=<id-of-{n}>"
        )
    cmds.append(
        f"gh label create {label_name!r} --color 0075ca"
        f" --description 'Ready for autonomous agent'  (idempotent)"
    )
    cmds.append(f"gh issue edit <new-number> --add-label {label_name!r}")
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
    body = render_body(raw, canonical_json)

    # BR-007: fail-closed body size check before any write
    if len(body) > GITHUB_BODY_LIMIT:
        print(
            f"BODY_TOO_LARGE: Rendered body is {len(body)} chars; "
            f"limit is {GITHUB_BODY_LIMIT}",
            file=sys.stderr,
        )
        return 1

    title = raw["title"]
    config = _load_config(cwd)
    label_name = get_label_name(config)

    # --dry-run: print and exit without any write (AC-007)
    if dry_run:
        print("=== Rendered Body ===\n")
        print(body)
        print("\n=== Planned gh Commands ===\n")
        for cmd in _planned_commands(title, blocked_by, label_name):
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

    # Step 2: blocked_by relations (SC-002, BR-004) — one gh api call per number.
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

    # Step 3: label — LAST so a partially created issue is never takeable (BR-005, SC-003)
    try:
        _ensure_label(gh, label_name)
        gh.run(["issue", "edit", issue_number, "--add-label", label_name])
    except subprocess.CalledProcessError:
        print(
            f"PARTIAL_FAILURE: Issue #{issue_number} created at {issue_url}",
            file=sys.stderr,
        )
        print(
            f"Unfinished steps: label '{label_name}' must be attached manually",
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
