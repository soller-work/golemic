"""Tests for P2-1: Semantic validation runs everywhere."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest


SCRIPTS_DIR = Path(__file__).parent.parent
SLICE_PY = SCRIPTS_DIR / "slice.py"
CREATE_ISSUE_PY = SCRIPTS_DIR / "create_issue.py"


def run_slice_cmd(*args):
    """Run slice.py command."""
    result = subprocess.run(
        [sys.executable, str(SLICE_PY)] + list(args),
        cwd=str(SCRIPTS_DIR.parent),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


def run_create_issue_cmd(*args):
    """Run create_issue.py command."""
    result = subprocess.run(
        [sys.executable, str(CREATE_ISSUE_PY)] + list(args),
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
        "success_outcome": "Done",
        "tldr": "Short",
        "scope": {"in": ["A"], "out": []},
        "behavior": "Do it.",
        "business_rules": "",
        "acceptance_scenarios": [],
        "inputs_outputs_errors": "X → Y",
        "verify_commands": [],
        "definition_of_done": [],
        "readiness": "blocked",
        "blockers": [{"kind": "question", "text": "Is this needed?"}],
        "security_relevant": False,
    }


class TestSemanticValidationEverywhere:
    """P2-1: Semantic validation in check, write, set, finalize, create_issue --dry-run."""

    def test_check_rejects_ready_with_blockers(self):
        """check rejects ready slice with non-empty blockers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = [{"kind": "question", "text": "Unresolved?"}]
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("check", str(slice_path))
            assert code != 0, "check should reject ready with blockers"
            assert "blockers" in stderr.lower()

    def test_check_rejects_security_relevant_without_content(self):
        """check rejects security_relevant=true without security content."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = []
            data["security_relevant"] = True
            data["security"] = ""  # empty
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_slice_cmd("check", str(slice_path))
            assert code != 0, "check should reject security_relevant without content"
            assert "security" in stderr.lower()

    def test_write_rejects_blocked_without_blockers(self):
        """write rejects blocked slice without blockers."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "blocked"
            data["blockers"] = []  # semantic error: blocked needs blocker
            json_str = json.dumps(data)

            code, stdout, stderr = run_slice_cmd("write", str(slice_path), json_str)
            assert code != 0, "write should reject blocked without blockers"

    def test_set_rejects_semantic_error(self):
        """set rejects when result violates semantic rules."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            slice_path.write_text(json.dumps(data))

            # Try to set readiness to ready while blockers are non-empty
            code, stdout, stderr = run_slice_cmd(
                "set", str(slice_path), "readiness", '"ready"'
            )
            assert code != 0, "set should reject ready with non-empty blockers"

    def test_create_issue_dry_run_rejects_semantic_error(self):
        """create_issue.py --dry-run rejects invalid slices."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = [{"kind": "question", "text": "Unresolved"}]
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_cmd(str(slice_path), "--dry-run")
            assert code != 0, "create_issue --dry-run should reject ready with blockers"
            assert "Validation failed" in stderr or "validation" in stderr.lower()


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
