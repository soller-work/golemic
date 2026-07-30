"""Tests for E2E mandatory scenarios / waiver mechanics (issue #304)."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest

SCRIPTS_DIR = Path(__file__).parent.parent
SLICE_PY = SCRIPTS_DIR / "slice.py"
CREATE_ISSUE_PY = SCRIPTS_DIR / "create_issue.py"
SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"


def run_slice(*args):
    return subprocess.run(
        [sys.executable, str(SLICE_PY)] + list(args),
        capture_output=True, text=True,
    )


def run_create_issue(*args):
    return subprocess.run(
        [sys.executable, str(CREATE_ISSUE_PY)] + list(args),
        capture_output=True, text=True,
    )


def _base_feature():
    return {
        "slice_type": "command",
        "change_type": "feature",
        "title": "Test Feature",
        "stakeholder": "User",
        "trigger": "Action occurs.",
        "success_outcome": "State updated.",
        "tldr": "Test feature slice.",
        "scope": {"in": ["A"], "out": []},
        "behavior": "Do it.",
        "business_rules": "",
        "acceptance_scenarios": [],
        "inputs_outputs_errors": "In: X. Out: Y.",
        "proof": {
            "how": "We observe the change.",
            "why": "The change is the promise.",
            "checks": [{"functional": "X changes", "technical": "A test asserts X changes"}],
        },
        "verify_commands": [],
        "definition_of_done": [],
        "readiness": "ready",
        "blockers": [],
        "security_relevant": False,
        "e2e_modify_approved": False,
    }


def check(data):
    with tempfile.TemporaryDirectory() as tmp:
        p = Path(tmp) / "slice.json"
        p.write_text(json.dumps(data))
        r = run_slice("check", str(p))
        return r.returncode, r.stdout, r.stderr


def dry_run(data):
    with tempfile.TemporaryDirectory() as tmp:
        p = Path(tmp) / "slice.json"
        p.write_text(json.dumps(data))
        r = run_create_issue(str(p), "--dry-run")
        return r.returncode, r.stdout, r.stderr


def finalize(data):
    with tempfile.TemporaryDirectory() as tmp:
        p = Path(tmp) / "slice.json"
        p.write_text(json.dumps(data))
        r = run_slice("finalize", str(p))
        return r.returncode, r.stdout, r.stderr


class TestFeatureE2ERequirement:
    def test_feature_without_e2e_fails_validation(self):
        """Feature slice with neither scenarios nor waiver must fail."""
        data = _base_feature()
        # no e2e field at all
        code, _, stderr = check(data)
        assert code != 0
        assert "e2e" in stderr.lower()

    def test_feature_with_empty_e2e_fails_validation(self):
        """Feature slice with e2e:{} (no scenarios, no waiver) must fail."""
        data = _base_feature()
        data["e2e"] = {}
        code, _, stderr = check(data)
        assert code != 0
        assert "e2e" in stderr.lower()

    def test_feature_with_scenarios_passes(self):
        """Feature slice with non-empty scenarios passes validation."""
        data = _base_feature()
        data["e2e"] = {"scenarios": [
            {"name": "issue_labels_sync", "description": "Sync labels.", "expected_outcome": "Labels updated."}
        ]}
        code, _, stderr = check(data)
        assert code == 0, f"Should pass: {stderr}"

    def test_feature_with_granted_waiver_passes(self):
        """Feature slice with granted waiver passes validation."""
        data = _base_feature()
        data["e2e"] = {"waiver": {"granted": True, "reason": "CLI output formatting only; no orchestration-loop involvement."}}
        code, _, stderr = check(data)
        assert code == 0, f"Should pass: {stderr}"

    def test_feature_with_both_scenarios_and_waiver_fails(self):
        """Feature slice with both scenarios and granted waiver must fail."""
        data = _base_feature()
        data["e2e"] = {
            "scenarios": [{"name": "happy_path", "description": "Do X.", "expected_outcome": "Y."}],
            "waiver": {"granted": True, "reason": "Should not combine."},
        }
        code, _, stderr = check(data)
        assert code != 0
        assert "both" in stderr.lower() or "either" in stderr.lower()

    def test_feature_finalize_without_e2e_does_not_become_ready(self):
        """finalize must fail (not silently downgrade) when feature has no E2E plan."""
        data = _base_feature()
        # no e2e
        code, _, stderr = finalize(data)
        assert code != 0
        assert "e2e" in stderr.lower()


class TestNonFeatureE2EBehavior:
    def _base_bug(self):
        data = {
            "slice_type": "command",
            "change_type": "bug",
            "title": "Fix bug",
            "stakeholder": "User",
            "trigger": "Crash occurs.",
            "success_outcome": "No crash.",
            "tldr": "Fix crash.",
            "scope": {"in": ["Handler"], "out": []},
            "reproduction": "Do X. Observed: crash. Expected: ok.",
            "root_cause": "Missing nil check.",
            "regression_scenarios": ["Given X, When Y, Then no crash."],
            "proof": {
                "how": "Run the trigger and see no crash.",
                "why": "No crash proves the fix.",
                "checks": [{"functional": "No crash", "technical": "A test asserts no error"}],
            },
            "verify_commands": [],
            "definition_of_done": [],
            "readiness": "ready",
            "blockers": [],
            "security_relevant": False,
        }
        return data

    def test_bug_without_e2e_passes(self):
        """Bug slice with no e2e field is valid."""
        data = self._base_bug()
        code, _, stderr = check(data)
        assert code == 0, f"Bug slice without e2e must be valid: {stderr}"

    def test_bug_with_e2e_scenarios_passes(self):
        """Bug slice with e2e.scenarios is allowed."""
        data = self._base_bug()
        data["e2e"] = {"scenarios": [{"name": "regression_check", "description": "Trigger bug path.", "expected_outcome": "No crash."}]}
        code, _, stderr = check(data)
        assert code == 0, f"Bug slice with e2e scenarios must be valid: {stderr}"

    def test_bug_with_waiver_fails(self):
        """Bug slice with e2e.waiver is not allowed (waivers are feature-only)."""
        data = self._base_bug()
        data["e2e"] = {"waiver": {"granted": True, "reason": "Not a feature."}}
        code, _, stderr = check(data)
        assert code != 0
        assert "waiver" in stderr.lower() or "feature" in stderr.lower()


class TestSchemaValidation:
    def test_bad_scenario_name_rejected(self):
        """Scenario name not matching ^[a-z][a-z0-9_]*$ must be rejected by schema."""
        data = _base_feature()
        data["e2e"] = {"scenarios": [{"name": "Bad-Name", "description": "X.", "expected_outcome": "Y."}]}
        code, _, stderr = check(data)
        assert code != 0
        assert "Bad-Name" in stderr or "pattern" in stderr.lower() or "name" in stderr.lower()

    def test_scenario_name_with_leading_digit_rejected(self):
        """Scenario name starting with digit must be rejected."""
        data = _base_feature()
        data["e2e"] = {"scenarios": [{"name": "1invalid", "description": "X.", "expected_outcome": "Y."}]}
        code, _, stderr = check(data)
        assert code != 0

    def test_valid_snake_case_name_accepted(self):
        """Valid snake_case scenario name is accepted."""
        data = _base_feature()
        data["e2e"] = {"scenarios": [{"name": "valid_scenario_name2", "description": "X.", "expected_outcome": "Y."}]}
        code, _, stderr = check(data)
        assert code == 0, f"Valid name should be accepted: {stderr}"

    def test_e2e_modify_approved_defaults_false_in_skeleton(self):
        """New feature skeleton includes e2e_modify_approved=false."""
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "slice.json"
            r = run_slice("new", "command", str(p))
            assert r.returncode == 0
            data = json.loads(p.read_text())
            assert data.get("e2e_modify_approved") is False

    def test_feature_skeleton_has_e2e_field(self):
        """New feature skeleton includes e2e block."""
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "slice.json"
            r = run_slice("new", "command", str(p))
            assert r.returncode == 0
            data = json.loads(p.read_text())
            assert "e2e" in data

    def test_bug_skeleton_has_no_e2e_field(self):
        """New bug skeleton does not include e2e block."""
        with tempfile.TemporaryDirectory() as tmp:
            p = Path(tmp) / "slice.json"
            r = run_slice("new", "command", "--change-type", "bug", str(p))
            assert r.returncode == 0
            data = json.loads(p.read_text())
            assert "e2e" not in data


class TestE2ERendering:
    def test_scenarios_render_e2e_scenarios_section(self):
        """Slice with e2e.scenarios renders ## E2E Scenarios section."""
        data = _base_feature()
        data["e2e"] = {"scenarios": [
            {"name": "issue_labels_sync", "description": "Sync issue labels.", "expected_outcome": "Labels match source."}
        ]}
        data["readiness"] = "ready"
        code, stdout, stderr = dry_run(data)
        assert code == 0, f"dry-run should succeed: {stderr}"
        assert "## E2E Scenarios" in stdout
        assert "issue_labels_sync" in stdout

    def test_waiver_renders_e2e_waiver_section(self):
        """Slice with e2e.waiver renders ## E2E Waiver section with reason."""
        data = _base_feature()
        data["e2e"] = {"waiver": {"granted": True, "reason": "CLI output formatting only; no orchestration-loop involvement."}}
        data["readiness"] = "ready"
        code, stdout, stderr = dry_run(data)
        assert code == 0, f"dry-run should succeed: {stderr}"
        assert "## E2E Waiver" in stdout
        assert "CLI output formatting only" in stdout
        assert "## E2E Scenarios" not in stdout

    def test_e2e_modify_approved_emits_exact_marker_line(self):
        """e2e_modify_approved=true renders a line that is byte-exact E2E-MODIFY-APPROVED."""
        data = _base_feature()
        data["e2e"] = {"scenarios": [{"name": "happy_path", "description": "X.", "expected_outcome": "Y."}]}
        data["e2e_modify_approved"] = True
        data["readiness"] = "ready"
        code, stdout, stderr = dry_run(data)
        assert code == 0, f"dry-run should succeed: {stderr}"
        lines = stdout.splitlines()
        assert "E2E-MODIFY-APPROVED" in lines, "Marker must appear as its own line"
        # The line must be byte-exact (no prefix/suffix on the line)
        marker_lines = [l for l in lines if "E2E-MODIFY-APPROVED" in l]
        assert any(l == "E2E-MODIFY-APPROVED" for l in marker_lines), (
            f"Marker line must be exactly 'E2E-MODIFY-APPROVED', got: {marker_lines}"
        )

    def test_e2e_modify_approved_false_omits_marker(self):
        """e2e_modify_approved=false (default) must not emit the marker line."""
        data = _base_feature()
        data["e2e"] = {"scenarios": [{"name": "happy_path", "description": "X.", "expected_outcome": "Y."}]}
        data["e2e_modify_approved"] = False
        data["readiness"] = "ready"
        code, stdout, stderr = dry_run(data)
        assert code == 0, f"dry-run should succeed: {stderr}"
        assert "E2E-MODIFY-APPROVED" not in stdout
