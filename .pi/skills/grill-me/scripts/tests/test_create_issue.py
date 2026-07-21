"""Tests for create_issue.py v2."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest


SCRIPTS_DIR = Path(__file__).parent.parent
CREATE_ISSUE_PY = SCRIPTS_DIR / "create_issue.py"
SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"


def load_minimal_slice():
    """Create a minimal valid slice dict."""
    return {
        "slice_type": "command",
        "change_type": "feature",
        "title": "Test Issue",
        "stakeholder": "User",
        "trigger": "User action",
        "success_outcome": "State updated",
        "tldr": "Update state",
        "scope": {"in": ["Feature A"], "out": ["Feature B"]},
        "behavior": "Mutate state.",
        "business_rules": "",
        "acceptance_scenarios": ["Given X, when Y, then Z"],
        "inputs_outputs_errors": "Input: event. Output: state.",
        "verify_commands": ["pytest"],
        "definition_of_done": ["Tests pass"],
        "readiness": "ready",
        "blockers": [],
        "security_relevant": False,
    }


def run_create_issue_py(*args):
    """Run create_issue.py and return (returncode, stdout, stderr)."""
    result = subprocess.run(
        [sys.executable, str(CREATE_ISSUE_PY)] + list(args),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


class TestCreateIssueRender:
    """Tests for fixed body layout."""

    def test_dry_run_renders_tldr_header(self):
        """Test --dry-run renders TL;DR header with change_type, slice_type, and tldr."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0, f"dry-run should succeed: {stderr}"
            assert "TL;DR" in stdout
            assert "feature" in stdout
            assert "command" in stdout
            assert "Update state" in stdout

    def test_fixed_section_order_no_security(self):
        """Test all sections present in fixed order when security_relevant=false."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["security_relevant"] = False
            data["blockers"] = []
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0

            # Extract section headers
            lines = stdout.split("\n")
            sections = [l for l in lines if l.startswith("## ")]
            section_names = [s.replace("## ", "").strip() for s in sections]

            # Expected sections when security_relevant=false and blockers empty
            expected = [
                "Stakeholder & Intent",
                "Scope",
                "Behavior",
                "Business Rules",
                "Acceptance Scenarios",
                "Inputs / Outputs / Errors",
                "Codebase Evidence",
                "Verify",
                "Definition of Done",
            ]

            assert section_names == expected, f"Got sections: {section_names}"
            # Security and Blockers should NOT be present
            assert "Security" not in section_names
            assert "Blockers / Open Questions" not in section_names

    def test_fixed_section_order_with_security_and_blockers(self):
        """Test all sections present when security_relevant=true and blockers non-empty."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "blocked"
            data["security_relevant"] = True
            data["security"] = "Requires auth."
            data["blockers"] = [{"kind": "question", "text": "Is this needed?"}]
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0

            lines = stdout.split("\n")
            sections = [l for l in lines if l.startswith("## ")]
            section_names = [s.replace("## ", "").strip() for s in sections]

            expected = [
                "Stakeholder & Intent",
                "Scope",
                "Behavior",
                "Business Rules",
                "Acceptance Scenarios",
                "Inputs / Outputs / Errors",
                "Codebase Evidence",
                "Security",
                "Verify",
                "Definition of Done",
                "Blockers / Open Questions",
            ]

            assert section_names == expected, f"Got sections: {section_names}"
            assert "Security" in section_names
            assert "Blockers / Open Questions" in section_names

    def test_empty_sections_render_none_placeholder(self):
        """Test empty sections render _None_ placeholder."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["business_rules"] = ""  # empty
            data["verify_commands"] = []  # empty
            data["definition_of_done"] = []  # empty
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0

            # Check for _None_ placeholders in empty sections
            assert "_None_" in stdout

    def test_conditional_security_section_present(self):
        """Test Security section rendered when security_relevant=true."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "blocked"
            data["security_relevant"] = True
            data["security"] = "Requires auth."
            data["blockers"] = [{"kind": "blocker", "text": "Auth not implemented"}]
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0
            assert "## Security" in stdout
            assert "Requires auth" in stdout

    def test_conditional_security_section_absent(self):
        """Test Security section NOT rendered when security_relevant=false."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["security_relevant"] = False
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0
            lines = stdout.split("\n")
            security_sections = [l for l in lines if l.strip() == "## Security"]
            assert len(security_sections) == 0

    def test_dry_run_invalid_slice_rejected(self):
        """Test --dry-run fails on invalid slice (semantic validation)."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = [{"kind": "question", "text": "Unresolved"}]  # ready with blockers = invalid
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code != 0, "Should reject ready slice with non-empty blockers"
            assert "Validation failed" in stderr

    def test_missing_file(self):
        """Test --dry-run fails on missing file."""
        code, stdout, stderr = run_create_issue_py("/nonexistent/slice.json", "--dry-run")
        assert code != 0
        assert "File not found" in stderr


class TestCreateIssueArchiving:
    """Test JSON archiving (P1-1: move, not copy)."""

    def test_archiving_uses_move(self):
        """Verify that archive_slice uses shutil.move."""
        create_issue_code = CREATE_ISSUE_PY.read_text(encoding="utf-8")
        assert "shutil.move" in create_issue_code
        assert "def archive_slice" in create_issue_code


class TestChangeTypeToIssueType:
    """Tests for change_type → GitHub issue type mapping."""

    @pytest.mark.parametrize("change_type,expected_type", [
        ("feature", "Feature"),
        ("bug", "Bug"),
        ("refactoring", "Task"),
    ])
    def test_dry_run_shows_mapped_issue_type(self, change_type, expected_type):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["change_type"] = change_type
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0, f"dry-run failed: {stderr}"
            assert f"--type {expected_type!r}" in stdout


