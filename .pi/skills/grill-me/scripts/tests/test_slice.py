"""Tests for slice.py v2 CLI commands."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest


SCRIPTS_DIR = Path(__file__).parent.parent
SLICE_PY = SCRIPTS_DIR / "slice.py"
SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"


def run_slice_cmd(*args, cwd=None):
    """Run slice.py command and return (returncode, stdout, stderr)."""
    result = subprocess.run(
        [sys.executable, str(SLICE_PY)] + list(args),
        cwd=cwd or str(SCRIPTS_DIR.parent),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


def load_schema():
    """Load schema."""
    return json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))


def load_minimal_slice():
    """Create a minimal valid slice dict (ready, no blockers)."""
    return {
        "slice_type": "command",
        "change_type": "feature",
        "title": "Test Slice",
        "stakeholder": "User",
        "trigger": "User clicks button",
        "success_outcome": "State updated",
        "tldr": "Update user state",
        "scope": {"in": ["Feature A"], "out": ["Feature B"]},
        "behavior": "Mutate the user state when button is clicked.",
        "business_rules": "",
        "acceptance_scenarios": ["Given user is logged in, when they click button, then state updates"],
        "inputs_outputs_errors": "Input: click event. Output: updated state. Errors: none.",
        "proof": {
            "how": "We click the button and see the state update.",
            "why": "The update is the promised outcome.",
            "checks": [{"functional": "Click updates state", "technical": "A test asserts the state update after the click"}],
        },
        "verify_commands": ["pytest"],
        "definition_of_done": ["Tests pass", "Code reviewed"],
        "readiness": "ready",
        "blockers": [],
        "security_relevant": False,
    }


class TestSliceNew:
    """AC-1: slice.py new command creates skeleton without N/A sections."""

    def test_new_command_creates_skeleton(self):
        """Test slice.py new command creates valid skeleton."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            code, stdout, stderr = run_slice_cmd("new", "command", "--file", str(slice_path))
            assert code == 0, f"Failed to create skeleton: {stderr}"
            assert slice_path.exists(), "Slice file not created"

            data = json.loads(slice_path.read_text(encoding="utf-8"))
            assert data["slice_type"] == "command"
            assert data["change_type"] == "feature"
            assert data["title"] == ""
            assert data["readiness"] == "blocked"
            assert data["blockers"] == []

    def test_new_query_skeleton(self):
        """Test slice.py new query creates valid skeleton."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            code, stdout, stderr = run_slice_cmd("new", "query", "--file", str(slice_path))
            assert code == 0
            assert slice_path.exists()

            data = json.loads(slice_path.read_text(encoding="utf-8"))
            assert data["slice_type"] == "query"

    def test_new_invalid_type(self):
        """Test slice.py new with invalid type."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            code, stdout, stderr = run_slice_cmd("new", "invalid", "--file", str(slice_path))
            assert code != 0, "Should fail with invalid type"
            assert "Unknown slice_type" in stderr


class TestSliceWrite:
    """AC-2: slice.py write accepts full JSON and validates atomically."""

    def test_write_valid_json(self):
        """Test slice.py write with valid JSON."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            json_str = json.dumps(data)

            code, stdout, stderr = run_slice_cmd("write", str(slice_path), json_str)
            assert code == 0, f"Failed to write: {stderr}"
            assert slice_path.exists()

            loaded = json.loads(slice_path.read_text(encoding="utf-8"))
            assert loaded["title"] == "Test Slice"

    def test_write_from_file(self):
        """Test slice.py write with @file syntax."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"
            json_path = tmpdir / "data.json"

            data = load_minimal_slice()
            json_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("write", str(slice_path), f"@{str(json_path)}")
            assert code == 0, f"Failed to write from file: {stderr}"
            assert slice_path.exists()

    def test_write_invalid_json(self):
        """Test slice.py write with invalid JSON."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            code, stdout, stderr = run_slice_cmd("write", str(slice_path), "{invalid json}")
            assert code != 0, "Should fail on invalid JSON"

    def test_write_invalid_schema(self):
        """Test slice.py write with JSON that fails schema validation."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["slice_type"] = "invalid_type"
            json_str = json.dumps(data)

            code, stdout, stderr = run_slice_cmd("write", str(slice_path), json_str)
            assert code != 0, "Should fail schema validation"
            assert "Validation failed" in stderr


class TestSliceCheck:
    """AC-3: slice.py check validates without modifying."""

    def test_check_valid_slice(self):
        """Test slice.py check on valid slice."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("check", str(slice_path))
            assert code == 0, f"Check should pass for valid slice: {stderr}"
            assert "is valid" in stdout

    def test_check_invalid_slice(self):
        """Test slice.py check on invalid slice."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["slice_type"] = "invalid_type"
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("check", str(slice_path))
            assert code != 0, "Check should fail for invalid slice"

    def test_check_does_not_modify(self):
        """Test that check does not modify the file."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            slice_path.write_text(json.dumps(data))
            original_mtime = slice_path.stat().st_mtime

            import time
            time.sleep(0.01)

            run_slice_cmd("check", str(slice_path))
            new_mtime = slice_path.stat().st_mtime

            assert original_mtime == new_mtime, "Check should not modify the file"


class TestSliceFinalize:
    """AC-4 & AC-5: slice.py finalize sets readiness correctly."""

    def test_finalize_auto_ready(self):
        """AC-4: finalize transitions blocked→ready when all content filled but no blockers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            # Start with what 'new' creates: readiness=blocked, blockers=[]
            data = load_minimal_slice()
            # Simulate fresh slice from 'new': blocked with no blockers
            data["readiness"] = "blocked"
            data["blockers"] = []
            slice_path.write_text(json.dumps(data))

            # finalize should transition to ready (normalize before validate)
            code, stdout, stderr = run_slice_cmd("finalize", str(slice_path))
            assert code == 0, f"Finalize should succeed: {stderr}"
            assert "ready" in stdout.lower(), f"Output: {stdout}"

            loaded = json.loads(slice_path.read_text(encoding="utf-8"))
            assert loaded["readiness"] == "ready", f"Should transition to ready, got {loaded['readiness']}"

    def test_finalize_blocked_flag(self):
        """AC-5: finalize --blocked forces readiness=blocked."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            # Add a blocker and set to blocked first (valid state)
            data["blockers"] = [{"kind": "question", "text": "Is this needed?"}]
            data["readiness"] = "blocked"
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("finalize", str(slice_path), "--blocked")
            assert code == 0, f"Finalize should succeed: {stderr}"

            loaded = json.loads(slice_path.read_text(encoding="utf-8"))
            assert loaded["readiness"] == "blocked"

    def test_finalize_with_blockers(self):
        """Test finalize sets blocked when blockers present."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["blockers"] = [{"kind": "question", "text": "Is this needed?"}]
            data["readiness"] = "blocked"
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("finalize", str(slice_path))
            assert code == 0

            loaded = json.loads(slice_path.read_text(encoding="utf-8"))
            assert loaded["readiness"] == "blocked"

    def test_finalize_invalid_slice(self):
        """Test finalize fails on invalid slice."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["title"] = ""  # invalid: title required
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("finalize", str(slice_path))
            assert code != 0, "Should fail on invalid slice"

    def test_finalize_blocked_without_blockers_fails(self):
        """P2: finalize --blocked with no blockers exits non-zero with actionable message."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["blockers"] = []  # No blockers
            data["readiness"] = "ready"
            slice_path.write_text(json.dumps(data))

            # Try finalize --blocked: should fail
            code, stdout, stderr = run_slice_cmd("finalize", str(slice_path), "--blocked")
            assert code != 0, "finalize --blocked should fail with no blockers"
            assert "blockers" in stderr.lower(), f"Error message should mention blockers: {stderr}"
            assert "slice.py set" in stderr, "Error should suggest how to add blockers"

    def test_finalize_blocked_with_valid_blocker_succeeds(self):
        """P2: finalize --blocked succeeds when blockers are present."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["blockers"] = [{"kind": "blocker", "text": "Awaiting design review"}]
            data["readiness"] = "ready"
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("finalize", str(slice_path), "--blocked")
            assert code == 0, f"finalize --blocked should succeed with blockers: {stderr}"

            loaded = json.loads(slice_path.read_text(encoding="utf-8"))
            assert loaded["readiness"] == "blocked"


class TestLineCount:
    """AC-9 & AC-10: Check file line counts."""

    def test_skill_md_line_count(self):
        """AC-9: SKILL.md is under 100 lines."""
        skill_md = SCRIPTS_DIR.parent / "SKILL.md"
        line_count = len(skill_md.read_text(encoding="utf-8").splitlines())
        assert line_count < 100, f"SKILL.md is {line_count} lines, should be <100"

    def test_schema_line_count(self):
        """AC-10: schema.json is under 250 lines."""
        schema_json = SCRIPTS_DIR.parent / "schema.json"
        line_count = len(schema_json.read_text(encoding="utf-8").splitlines())
        assert line_count < 250, f"schema.json is {line_count} lines, should be <250"




if __name__ == "__main__":
    pytest.main([__file__, "-v"])

if __name__ == "__main__":
    pytest.main([__file__, "-v"])
