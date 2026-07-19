#!/usr/bin/env python3
"""Fetch open GitHub issues for similarity scanning during grill-me interviews.

Usage:
    python3 gh_issue_index.py [--with-body]

Outputs compact JSON with number, title, labels. Includes body only with --with-body.
Exit code 0 on success, 1 on error.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys


def run_gh(args: list[str], capture_output: bool = False) -> subprocess.CompletedProcess:
    """Run gh command."""
    return subprocess.run(
        ["gh"] + args,
        check=False,
        capture_output=capture_output,
        text=True,
    )


def fetch_issues(with_body: bool = False) -> list[dict]:
    """Fetch open issues from GitHub using gh CLI."""
    fields = "number,title,labels,body" if with_body else "number,title,labels"
    
    result = run_gh(
        ["issue", "list", "--state", "open", "--json", fields, "--limit", "200"],
        capture_output=True,
    )

    if result.returncode != 0:
        print(f"❌ Failed to fetch issues: {result.stderr}", file=sys.stderr)
        sys.exit(1)

    try:
        issues = json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        print(f"❌ Failed to parse gh output: {exc}", file=sys.stderr)
        sys.exit(1)

    return issues


def compact_issue(issue: dict, with_body: bool = False) -> dict:
    """Compact issue to essential fields."""
    compact = {
        "number": issue.get("number"),
        "title": issue.get("title"),
        "labels": [label.get("name", "") for label in issue.get("labels", [])],
    }
    if with_body:
        compact["body"] = issue.get("body", "")
    return compact


def main() -> int:
    """CLI entry point."""
    parser = argparse.ArgumentParser(
        description="Fetch open GitHub issues for similarity scanning"
    )
    parser.add_argument(
        "--with-body",
        action="store_true",
        help="Include issue body in output (slows down fetching)",
    )
    args = parser.parse_args()

    issues = fetch_issues(with_body=args.with_body)
    compacted = [compact_issue(issue, with_body=args.with_body) for issue in issues]

    # Output as compact single-line JSON
    print(json.dumps(compacted, separators=(",", ":"), ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
