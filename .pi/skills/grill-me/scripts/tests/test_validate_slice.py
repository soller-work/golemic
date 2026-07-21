"""Tests for validate_slice.py."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest


SCRIPTS_DIR = Path(__file__).parent.parent
VALIDATE_PY = SCRIPTS_DIR / "validate_slice.py"
SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"


def run_validate(*args):
    """Run validate_slice.py and return (returncode, stdout, stderr)."""
    result = subprocess.run(
        [sys.executable, str(VALIDATE_PY)] + list(args),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


def load_minimal_slice():
    """Create a minimal valid slice dict."""
    return {
        "slice_type": "command",
        "change_type": "feature",
        "title": "Test",
        "stakeholder": "User",
        "trigger": "Action",
        "success_outcome": "Result",
        "tldr": "Short",
        "scope": {"in": ["A"], "out": []},
        "behavior": "Do something.",
        "business_rules": "",
        "acceptance_scenarios": [],
        "inputs_outputs_errors": "Input: X. Output: Y.",
        "verify_commands": [],
        "definition_of_done": [],
        "readiness": "blocked",
        "blockers": [{"kind": "question", "text": "Is this needed?"}],
        "security_relevant": False,
    }


class TestValidateSlice:
    """Tests for semantic validation of slice.json v2."""

    def test_validate_minimal_slice(self):
        """Test validation of minimal valid slice."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code == 0, f"Should validate: {stderr}"

    def test_validate_ready_with_empty_blockers(self):
        """Test validation passes for ready slice with empty blockers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code == 0

    def test_validate_ready_with_blockers_fails(self):
        """Test validation fails for ready slice with blockers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = [{"kind": "question", "text": "Need clarification?"}]
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code != 0, "Should fail: ready with blockers"

    def test_validate_blocked_without_blockers_fails(self):
        """Test validation fails for blocked slice with no blockers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            data["readiness"] = "blocked"
            data["blockers"] = []
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code != 0, "Should fail: blocked with no blockers"

    def test_validate_security_relevant_without_security_fails(self):
        """Test validation fails when security_relevant=true but no security field."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            data["security_relevant"] = True
            # Don't set security field
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code != 0, "Should fail: security_relevant=true without security content"

    def test_validate_placeholder_tokens_in_ready_fails(self):
        """Test validation fails for ready slice with unquoted placeholder tokens."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            data["behavior"] = "Do something, TBD later."  # unquoted TBD
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code != 0, "Should fail: unquoted placeholder token in ready slice"

    def test_validate_quoted_placeholder_allowed(self):
        """Test validation passes when placeholder is quoted."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            data["behavior"] = "Return `TBD` as placeholder text."  # quoted TBD
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code == 0, "Should pass: quoted placeholder is allowed"

    def test_validate_missing_required_field(self):
        """Test validation fails when required field is missing."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            del data["title"]  # required field
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code != 0, "Should fail: missing required field"


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
