#!/usr/bin/env python3
"""Machine-readable dependency graph over backlog issues.

Each issue file declares `depends_on`: a list of `slice_id`s of other issues
that must be completed before it. An issue lives in one of three states:

  docs/backlog/NNN_*.json           open   — unclaimed, available to take
  docs/backlog/working/NNN_*.json   claimed — being worked on right now
  (file deleted)                    done   — completed (delete = done)

A claimed issue is not yet done, so it still blocks its dependents. Therefore
an issue is *takeable* when none of its `depends_on` slice_ids still exist
anywhere in the backlog (neither open nor claimed), and only the unclaimed
top-level issues are candidates to take.

Subcommands:
  takeable <dir>            List open (unclaimed) issues whose dependencies are
                            all satisfied, in processing (prefix) order.
  check <dir> <slice_id>    Exit 0 if the issue is takeable, else 1 and print
                            the dependencies that still block it.
  claim <dir>               Atomically move the lowest-prefix takeable issue
                            into <dir>/working/ and print its new path. This is
                            the mutual-exclusion primitive for parallel agents:
                            concurrent claimers never get the same issue.
  verify <dir>              Structural graph checks over all existing issues:
                            self-dependencies and cycles are hard errors
                            (exit 2); dependencies that resolve to no existing
                            issue are reported as warnings (assumed completed).
"""
from __future__ import annotations

import json
import sys
from pathlib import Path


class Issue:
    def __init__(self, path: Path, slice_id: str, depends_on: list[str]) -> None:
        self.path = path
        self.slice_id = slice_id
        self.depends_on = depends_on


def load_dir(directory: Path) -> list[Issue]:
    issues: list[Issue] = []
    if not directory.is_dir():
        return issues
    for path in sorted(directory.glob("[0-9][0-9][0-9]_*.json")):
        data = json.loads(path.read_text())
        issues.append(
            Issue(path, data["slice_id"], list(data.get("depends_on", [])))
        )
    return issues


def open_issues(backlog: Path) -> list[Issue]:
    """Unclaimed issues at the top level of the backlog."""
    return load_dir(backlog)


def all_issues(backlog: Path) -> list[Issue]:
    """Every issue that still exists: open plus claimed (working/)."""
    return load_dir(backlog) + load_dir(backlog / "working")


def existing_ids(backlog: Path) -> set[str]:
    """slice_ids that still exist (open or claimed) and therefore block deps."""
    return {i.slice_id for i in all_issues(backlog)}


def blocking_deps(issue: Issue, existing: set[str]) -> list[str]:
    """Dependencies that still exist (open or claimed) and block this issue."""
    return [dep for dep in issue.depends_on if dep in existing]


def takeable_issues(backlog: Path) -> list[Issue]:
    existing = existing_ids(backlog)
    return [i for i in open_issues(backlog) if not blocking_deps(i, existing)]


def cmd_takeable(backlog: Path) -> int:
    for issue in takeable_issues(backlog):
        print(f"{issue.path}\t{issue.slice_id}")
    return 0


def cmd_check(backlog: Path, slice_id: str) -> int:
    existing = existing_ids(backlog)
    target = next((i for i in open_issues(backlog) if i.slice_id == slice_id), None)
    if target is None:
        print(f"no open issue with slice_id {slice_id!r} in {backlog}", file=sys.stderr)
        return 1
    blockers = blocking_deps(target, existing)
    if blockers:
        print(f"blocked: {slice_id} waits on {', '.join(sorted(blockers))}", file=sys.stderr)
        return 1
    print(f"takeable: {slice_id}")
    return 0


def cmd_claim(backlog: Path) -> int:
    working = backlog / "working"
    working.mkdir(exist_ok=True)
    # Re-evaluate takeability on every attempt: a concurrent claimer may have
    # moved an issue into working/, changing what still blocks whom.
    while True:
        candidates = takeable_issues(backlog)
        if not candidates:
            print(
                f"no takeable issue in {backlog} (all open issues are blocked or claimed)",
                file=sys.stderr,
            )
            return 1
        issue = candidates[0]
        dest = working / issue.path.name
        try:
            issue.path.rename(dest)
        except (FileNotFoundError, OSError):
            # Lost the race for this file; another agent claimed it. Retry.
            continue
        print(f"{dest}\t{issue.slice_id}")
        return 0


def _find_cycle(issues: list[Issue], existing: set[str]) -> list[str] | None:
    graph = {i.slice_id: [d for d in i.depends_on if d in existing] for i in issues}
    WHITE, GRAY, BLACK = 0, 1, 2
    color = {sid: WHITE for sid in graph}

    def dfs(node: str, stack: list[str]) -> list[str] | None:
        color[node] = GRAY
        stack.append(node)
        for dep in graph[node]:
            if color.get(dep) == GRAY:
                return stack[stack.index(dep):] + [dep]
            if color.get(dep) == WHITE:
                found = dfs(dep, stack)
                if found:
                    return found
        color[node] = BLACK
        stack.pop()
        return None

    for sid in graph:
        if color[sid] == WHITE:
            found = dfs(sid, [])
            if found:
                return found
    return None


def cmd_verify(backlog: Path) -> int:
    issues = all_issues(backlog)
    existing = {i.slice_id for i in issues}
    errors: list[str] = []
    warnings: list[str] = []

    for issue in issues:
        if issue.slice_id in issue.depends_on:
            errors.append(f"{issue.slice_id} depends on itself")
        for dep in issue.depends_on:
            if dep not in existing:
                warnings.append(
                    f"{issue.slice_id} depends on {dep!r}, which is not an "
                    f"existing issue (assumed completed)"
                )

    cycle = _find_cycle(issues, existing)
    if cycle:
        errors.append("dependency cycle: " + " -> ".join(cycle))

    for w in warnings:
        print(f"warning: {w}", file=sys.stderr)
    for e in errors:
        print(f"error: {e}", file=sys.stderr)
    if errors:
        return 2
    print(f"ok: {len(issues)} existing issue(s), dependency graph is acyclic")
    return 0


def main(argv: list[str]) -> int:
    if len(argv) < 3:
        print(__doc__, file=sys.stderr)
        return 1
    command, backlog = argv[1], Path(argv[2])
    if not backlog.is_dir():
        print(f"not a directory: {backlog}", file=sys.stderr)
        return 1
    if command == "takeable":
        return cmd_takeable(backlog)
    if command == "check":
        if len(argv) < 4:
            print("check requires a slice_id argument", file=sys.stderr)
            return 1
        return cmd_check(backlog, argv[3])
    if command == "claim":
        return cmd_claim(backlog)
    if command == "verify":
        return cmd_verify(backlog)
    print(f"unknown command {command!r}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
