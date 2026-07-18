"""Guard test: validates every references/*-slice.json against schema.json plus semantic checks.

Discovered dynamically so future example files are covered automatically (BR-005).
"""
from __future__ import annotations

import sys
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator

SKILL_DIR = Path(__file__).parent.parent.parent
SCHEMA_PATH = SKILL_DIR / "schema.json"
REFERENCES_DIR = SKILL_DIR / "references"

sys.path.insert(0, str(SKILL_DIR / "scripts"))

from validate_slice import load_json, semantic_errors  # noqa: E402

_EXAMPLE_FILES = sorted(REFERENCES_DIR.glob("*-slice.json"))


def _collect_errors(schema: dict, document: dict) -> list[str]:
    validator = Draft202012Validator(schema)
    structural = sorted(validator.iter_errors(document), key=lambda e: list(e.absolute_path))
    semantic = semantic_errors(document) if not structural else []
    return [
        *[f"$.{'.'.join(str(p) for p in e.absolute_path)}: {e.message}" for e in structural],
        *semantic,
    ]


@pytest.mark.parametrize("example_path", _EXAMPLE_FILES, ids=lambda p: p.name)
def test_example_validates(example_path: Path) -> None:
    schema = load_json(SCHEMA_PATH)
    document = load_json(example_path)
    errors = _collect_errors(schema, document)
    assert not errors, (
        f"{example_path.name} failed validation:\n"
        + "\n".join(f"  - {e}" for e in errors)
    )


def test_broken_example_is_detected() -> None:
    """Guard logic must surface validation errors and name the offending field."""
    schema = load_json(SCHEMA_PATH)
    broken = load_json(REFERENCES_DIR / "example-slice.json")
    del broken["title"]
    errors = _collect_errors(schema, broken)
    assert errors, "Expected validation errors for a document missing required field 'title'"
    assert any("title" in e for e in errors), (
        f"Error message must name the missing field 'title'; got:\n"
        + "\n".join(f"  - {e}" for e in errors)
    )
