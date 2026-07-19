"""Guard test: every references/*-slice.json must pass full validation (structural + semantic)."""
from __future__ import annotations

import sys
from pathlib import Path

import pytest

SKILL_DIR = Path(__file__).parent.parent.parent
REFERENCES_DIR = SKILL_DIR / "references"

sys.path.insert(0, str(SKILL_DIR / "scripts"))

from validate_slice import full_validate, load_json, load_schema  # noqa: E402

_EXAMPLE_FILES = sorted(REFERENCES_DIR.glob("*-slice.json"))


@pytest.mark.parametrize("example_path", _EXAMPLE_FILES, ids=lambda p: p.name)
def test_example_validates(example_path: Path) -> None:
    schema = load_schema()
    document = load_json(example_path)
    errors = full_validate(document, schema)
    assert not errors, (
        f"{example_path.name} failed validation:\n"
        + "\n".join(f"  - {e}" for e in errors)
    )


def test_broken_example_is_detected() -> None:
    """Guard logic must surface validation errors for a document missing a required field."""
    schema = load_schema()
    broken = load_json(REFERENCES_DIR / "example-slice.json")
    del broken["title"]
    errors = full_validate(broken, schema)
    assert errors, "Expected validation errors for a document missing required field 'title'"
    assert any("title" in e for e in errors), (
        "Error message must name the missing field 'title'; got:\n"
        + "\n".join(f"  - {e}" for e in errors)
    )
