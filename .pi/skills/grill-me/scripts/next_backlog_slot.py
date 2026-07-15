#!/usr/bin/env python3
"""Compute the target path for a new backlog issue: next free number + slug.

Usage: next_backlog_slot.py <title words...> [--dir <backlog-dir>]
Prints e.g. docs/backlog/012_event-log-writer.json
"""
import re
import sys
from pathlib import Path


def main() -> int:
    args = sys.argv[1:]
    backlog = Path("docs/backlog")
    if "--dir" in args:
        i = args.index("--dir")
        backlog = Path(args[i + 1])
        del args[i : i + 2]
    if not args:
        print("usage: next_backlog_slot.py <title words...> [--dir <backlog-dir>]", file=sys.stderr)
        return 1
    slug = re.sub(r"[^a-z0-9]+", "-", " ".join(args).lower()).strip("-")
    if not slug:
        print("title produced an empty slug", file=sys.stderr)
        return 1
    numbers = [int(p.name[:3]) for p in backlog.glob("[0-9][0-9][0-9]_*.json")]
    print(backlog / f"{max(numbers, default=0) + 1:03d}_{slug}.json")
    return 0


if __name__ == "__main__":
    sys.exit(main())
