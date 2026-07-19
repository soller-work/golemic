"""Guard test: every reference example under references/ passes semantic validation."""

import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))
from validate_slice import semantic_errors

REFERENCES_DIR = Path(__file__).parent.parent.parent / "references"


def reference_examples():
    return sorted(REFERENCES_DIR.glob("example-*.json"))


@pytest.mark.parametrize("example_path", reference_examples(), ids=lambda p: p.name)
def test_reference_example_passes_semantic_validation(example_path):
    doc = json.loads(example_path.read_text(encoding="utf-8"))
    findings = semantic_errors(doc)
    assert findings == [], f"{example_path.name} has semantic errors:\n" + "\n".join(
        f"  - {e}" for e in findings
    )
