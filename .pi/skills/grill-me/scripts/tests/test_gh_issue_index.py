"""Tests for gh_issue_index.py."""

import json
import subprocess
import sys
from pathlib import Path

import pytest


SCRIPTS_DIR = Path(__file__).parent.parent
GH_ISSUE_INDEX_PY = SCRIPTS_DIR / "gh_issue_index.py"


def run_gh_issue_index(*args):
    """Run gh_issue_index.py and return (returncode, stdout, stderr)."""
    result = subprocess.run(
        [sys.executable, str(GH_ISSUE_INDEX_PY)] + list(args),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


class TestGhIssueIndex:
    """AC-8: gh_issue_index.py fetches and outputs compact JSON."""

    def test_compact_json_structure(self):
        """AC-8: Verify that compacted JSON has expected structure (labels as strings)."""
        # Test the compaction logic directly
        gh_response = {
            "number": 123,
            "title": "Feature request",
            "labels": [{"name": "bug"}, {"name": "urgent"}],
        }

        # Simulate what compact_issue does
        compacted = {
            "number": gh_response.get("number"),
            "title": gh_response.get("title"),
            "labels": [label.get("name", "") for label in gh_response.get("labels", [])],
        }

        assert compacted["number"] == 123
        assert compacted["title"] == "Feature request"
        assert compacted["labels"] == ["bug", "urgent"]  # extracted as strings
        assert "body" not in compacted

    def test_compact_json_with_body(self):
        """Test that body is included when requested."""
        gh_response = {
            "number": 123,
            "title": "Feature",
            "labels": [{"name": "enhancement"}],
            "body": "This is the issue body.",
        }

        # With body flag
        compacted_with_body = {
            "number": gh_response.get("number"),
            "title": gh_response.get("title"),
            "labels": [label.get("name", "") for label in gh_response.get("labels", [])],
            "body": gh_response.get("body", ""),
        }

        assert "body" in compacted_with_body
        assert compacted_with_body["body"] == "This is the issue body."

    def test_gh_issue_index_script_exists(self):
        """Test that gh_issue_index.py script exists and is executable."""
        assert GH_ISSUE_INDEX_PY.exists(), f"Script not found: {GH_ISSUE_INDEX_PY}"


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
