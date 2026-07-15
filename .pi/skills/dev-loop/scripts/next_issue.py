#!/usr/bin/env python3
"""Print the next open backlog issue (lowest numeric prefix) from docs/backlog/."""
import json
import sys
from pathlib import Path


def main() -> int:
    backlog = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("docs/backlog")
    files = sorted(backlog.glob("[0-9][0-9][0-9]_*.json"))
    if not files:
        print(f"no open issues in {backlog}", file=sys.stderr)
        return 1
    path = files[0]
    slice_data = json.loads(path.read_text())
    print(path)
    print(f"slice_id: {slice_data['slice_id']}")
    print(f"title: {slice_data['title']}")
    print(f"readiness: {slice_data['readiness']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
