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
        "proof": {
            "how": "We run it and observe Y.",
            "why": "Y is the promised result.",
            "checks": [{"functional": "Produces Y", "technical": "A test asserts Y"}],
        },
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
            data["behavior"] = "Return `TBD` as the marker text."  # quoted TBD
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code == 0, "Should pass: quoted placeholder is allowed"

    def test_validate_removed_words_pass_unquoted(self):
        """Test that 'unknown' and 'later' no longer trigger placeholder errors."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            schema_path = SCHEMA_PATH

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            data["behavior"] = "An unrecognized subcommand is treated the same as an unknown command. We handle it later in the pipeline."
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_validate(str(schema_path), str(slice_path))
            assert code == 0, f"Should pass: 'unknown'/'later' are ordinary prose words, not deferral markers. stderr={stderr}"

    def _check_marker_fails(self, marker_text: str):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = Path(tmpdir) / "slice.json"
            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            data["behavior"] = f"This field contains {marker_text} as a deferral marker."
            slice_path.write_text(json.dumps(data))
            code, stdout, stderr = run_validate(str(SCHEMA_PATH), str(slice_path))
            assert code != 0, f"Should fail: unquoted marker '{marker_text}' should block ready slice"

    def _check_marker_passes_when_backticked(self, marker_text: str):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = Path(tmpdir) / "slice.json"
            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            data["behavior"] = f"Return `{marker_text}` as literal text."
            slice_path.write_text(json.dumps(data))
            code, stdout, stderr = run_validate(str(SCHEMA_PATH), str(slice_path))
            assert code == 0, f"Should pass: backtick-quoted marker '{marker_text}' must not block ready slice"

    def test_new_markers_fail_unquoted(self):
        """Test that newly added deferral markers block a ready slice."""
        new_markers = [
            "tbc",
            "to be confirmed",
            "to be determined",
            "to be defined",
            "to be added",
            "placeholder",
            "xxx",
            "wip",
            "work in progress",
            "???",
        ]
        for marker in new_markers:
            self._check_marker_fails(marker)

    def test_new_markers_pass_when_backticked(self):
        """Test that backtick-quoted new markers are allowed in ready slices."""
        new_markers = ["tbc", "placeholder", "xxx", "???"]
        for marker in new_markers:
            self._check_marker_passes_when_backticked(marker)

    def test_existing_marker_todo_still_fails(self):
        """Test that existing marker 'todo' still blocks a ready slice."""
        self._check_marker_fails("todo")

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
