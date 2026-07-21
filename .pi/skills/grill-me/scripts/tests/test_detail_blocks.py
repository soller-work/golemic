"""Tests for gattung-specific detail blocks driven by a single registry."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path


SCRIPTS_DIR = Path(__file__).parent.parent
CREATE_ISSUE_PY = SCRIPTS_DIR / "create_issue.py"
SLICE_PY = SCRIPTS_DIR / "slice.py"


def run_slice(*args):
    result = subprocess.run(
        [sys.executable, str(SLICE_PY)] + list(args),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


def check_slice(data):
    with tempfile.TemporaryDirectory() as tmpdir:
        slice_path = Path(tmpdir) / "slice.json"
        slice_path.write_text(json.dumps(data))
        return run_slice("check", str(slice_path))

FEATURE_ONLY_HEADINGS = [
    "## Behavior",
    "## Business Rules",
    "## Acceptance Scenarios",
    "## Inputs / Outputs / Errors",
]


def headings_of(body):
    return [line for line in body.splitlines() if line.startswith("## ")]


def _common_core():
    return {
        "slice_type": "command",
        "title": "Test",
        "stakeholder": "User",
        "trigger": "Action",
        "success_outcome": "Done",
        "tldr": "Short",
        "scope": {"in": ["A"], "out": []},
        "proof": {
            "how": "We observe the change.",
            "why": "The observed change is the promise.",
            "checks": [{"functional": "It works", "technical": "A test asserts it"}],
        },
        "verify_commands": ["pytest"],
        "definition_of_done": ["Tests pass"],
        "readiness": "ready",
        "blockers": [],
        "security_relevant": False,
    }


def build_bug_slice():
    data = _common_core()
    data["change_type"] = "bug"
    data["reproduction"] = "1. Do X. Observed: crash. Expected: success."
    data["root_cause"] = "Missing nil check in handler."
    data["regression_scenarios"] = ["Given X, When Y, Then no crash"]
    return data


def build_refactoring_slice():
    data = _common_core()
    data["change_type"] = "refactoring"
    data["current_structure"] = "God object handles parsing and rendering."
    data["target_structure"] = "Split into Parser and Renderer."
    data["behavior_preservation"] = "Same output bytes for identical input."
    return data


def run_create_issue(*args):
    result = subprocess.run(
        [sys.executable, str(CREATE_ISSUE_PY)] + list(args),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


def render_body_of(data):
    with tempfile.TemporaryDirectory() as tmpdir:
        slice_path = Path(tmpdir) / "slice.json"
        slice_path.write_text(json.dumps(data))
        code, stdout, stderr = run_create_issue(str(slice_path), "--dry-run")
        assert code == 0, f"dry-run failed: {stderr}"
        return stdout


class TestBugDetailBlock:
    def test_bug_renders_its_sections_and_no_feature_sections(self):
        body = render_body_of(build_bug_slice())
        assert "## Reproduction" in body
        assert "## Root Cause" in body
        assert "## Regression Scenarios" in body
        assert "Missing nil check in handler." in body
        assert "Given X, When Y, Then no crash" in body
        headings = headings_of(body)
        for heading in FEATURE_ONLY_HEADINGS:
            assert heading not in headings, f"bug body must not contain {heading}"


class TestRefactoringDetailBlock:
    def test_refactoring_renders_its_sections_and_no_feature_sections(self):
        body = render_body_of(build_refactoring_slice())
        assert "## Current Structure" in body
        assert "## Target Structure" in body
        assert "## Behavior Preservation" in body
        assert "Split into Parser and Renderer." in body
        headings = headings_of(body)
        for heading in FEATURE_ONLY_HEADINGS:
            assert heading not in headings, f"refactoring body must not contain {heading}"


class TestGattungRequiredFields:
    def test_bug_missing_required_field_is_rejected(self):
        data = build_bug_slice()
        del data["regression_scenarios"]
        code, stdout, stderr = check_slice(data)
        assert code != 0, "bug slice missing regression_scenarios must be rejected"
        assert "regression_scenarios" in stderr

    def test_refactoring_missing_required_field_is_rejected(self):
        data = build_refactoring_slice()
        del data["target_structure"]
        code, stdout, stderr = check_slice(data)
        assert code != 0, "refactoring slice missing target_structure must be rejected"
        assert "target_structure" in stderr

    def test_foreign_gattung_field_is_rejected(self):
        data = build_bug_slice()
        data["behavior"] = "This feature-only field does not belong in a bug slice."
        code, stdout, stderr = check_slice(data)
        assert code != 0, "bug slice carrying a feature-only field must be rejected"
        assert "behavior" in stderr

    def test_feature_slice_still_valid(self):
        data = _common_core()
        data["change_type"] = "feature"
        data["behavior"] = "Mutate state."
        data["business_rules"] = ""
        data["acceptance_scenarios"] = ["Given X, When Y, Then Z"]
        data["inputs_outputs_errors"] = "In: event. Out: state."
        code, stdout, stderr = check_slice(data)
        assert code == 0, f"feature slice must stay valid: {stderr}"


class TestSkeleton:
    def test_new_bug_skeleton_has_bug_fields_only(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "slice.json"
            code, stdout, stderr = run_slice(
                "new", "command", "--change-type", "bug", "--file", str(path)
            )
            assert code == 0, f"new failed: {stderr}"
            data = json.loads(path.read_text())
            assert data["change_type"] == "bug"
            for key in ("reproduction", "root_cause", "regression_scenarios"):
                assert key in data, f"bug skeleton must seed {key}"
            for key in ("behavior", "business_rules", "acceptance_scenarios", "inputs_outputs_errors"):
                assert key not in data, f"bug skeleton must not seed feature field {key}"

    def test_new_defaults_to_feature_skeleton(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "slice.json"
            code, stdout, stderr = run_slice("new", "command", "--file", str(path))
            assert code == 0, f"new failed: {stderr}"
            data = json.loads(path.read_text())
            assert data["change_type"] == "feature"
            for key in ("behavior", "business_rules", "acceptance_scenarios", "inputs_outputs_errors"):
                assert key in data, f"feature skeleton must seed {key}"


class TestPlan:
    def test_plan_for_bug_slice_shows_bug_fields(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "slice.json"
            path.write_text(json.dumps(build_bug_slice()))
            code, stdout, stderr = run_slice("plan", str(path))
            assert code == 0, f"plan failed: {stderr}"
            assert "reproduction" in stdout
            assert "root_cause" in stdout
            assert "regression_scenarios" in stdout
            for feature_key in ("business_rules", "acceptance_scenarios", "inputs_outputs_errors"):
                assert feature_key not in stdout, f"bug plan must not list feature field {feature_key}"

    def test_plan_change_type_flag_shows_refactoring_fields(self):
        code, stdout, stderr = run_slice("plan", "--change-type", "refactoring")
        assert code == 0, f"plan failed: {stderr}"
        assert "current_structure" in stdout
        assert "target_structure" in stdout
        assert "behavior_preservation" in stdout

    def test_plan_defaults_to_feature(self):
        code, stdout, stderr = run_slice("plan")
        assert code == 0, f"plan failed: {stderr}"
        assert "acceptance_scenarios" in stdout
        assert "inputs_outputs_errors" in stdout
