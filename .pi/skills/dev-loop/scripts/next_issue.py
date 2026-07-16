#!/usr/bin/env python3
"""Print the next takeable backlog issue (lowest numeric prefix) from docs/backlog/.

An issue is takeable only when all of its `depends_on` slice_ids are completed
(no longer present anywhere in the backlog, including the working/ subdir).
Takeability and claiming live in the grill-me issue_graph.py script so the
dependency logic stays in one place.

Usage:
  next_issue.py [backlog_dir]            Preview the next takeable issue (read-only).
  next_issue.py [backlog_dir] --claim    Atomically claim it: move the file into
                                         backlog/working/ so no other agent takes
                                         it, then print it. Use this when actually
                                         starting work (safe for parallel agents).
"""
import json
import subprocess
import sys
from pathlib import Path

ISSUE_GRAPH = (
    Path(__file__).resolve().parent.parent.parent
    / "grill-me"
    / "scripts"
    / "issue_graph.py"
)


def run_graph(command: str, backlog: Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(ISSUE_GRAPH), command, str(backlog)],
        capture_output=True,
        text=True,
    )


def report(path: Path) -> None:
    data = json.loads(path.read_text())
    print(path)
    print(f"slice_id: {data['slice_id']}")
    print(f"title: {data['title']}")
    print(f"readiness: {data['readiness']}")
    print(f"depends_on: {data.get('depends_on', [])}")


def main() -> int:
    args = [a for a in sys.argv[1:] if a != "--claim"]
    claim = "--claim" in sys.argv[1:]
    backlog = Path(args[0]) if args else Path("docs/backlog")

    if claim:
        result = run_graph("claim", backlog)
        if result.returncode != 0:
            sys.stderr.write(result.stderr)
            return result.returncode
        moved_path = Path(result.stdout.split("\t")[0].strip())
        report(moved_path)
        return 0

    result = run_graph("takeable", backlog)
    if result.returncode != 0:
        sys.stderr.write(result.stderr)
        return result.returncode
    lines = [ln for ln in result.stdout.splitlines() if ln.strip()]
    if not lines:
        print(
            f"no takeable issue in {backlog} (all open issues are blocked or claimed)",
            file=sys.stderr,
        )
        return 1
    report(Path(lines[0].split("\t")[0]))
    return 0


if __name__ == "__main__":
    sys.exit(main())
