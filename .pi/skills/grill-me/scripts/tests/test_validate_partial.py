"""Tests for validate-slice-partial.py — covers AC-004, AC-005, AC-006."""

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS_DIR = Path(__file__).parent.parent
SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"


def _load_module():
    spec = importlib.util.spec_from_file_location(
        "validate_slice_partial", SCRIPTS_DIR / "validate-slice-partial.py"
    )
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


vp = _load_module()


def _run_stage(slice_data, stage):
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".json", delete=False
    ) as f:
        json.dump(slice_data, f)
        path = f.name
    result = subprocess.run(
        [
            sys.executable,
            str(SCRIPTS_DIR / "validate-slice-partial.py"),
            path,
            "--stage",
            stage,
        ],
        capture_output=True,
        text=True,
    )
    return result


_MINIMAL_SLICE = {
    "schema_version": "1.1.0",
    "slice_type": "command",
    "title": "Test slice",
    "summary": "A minimal slice for unit tests.",
    "readiness": "blocked",
    "stakeholder_intent": {
        "actor": "Tester",
        "goal": "Verify correctness",
        "business_value": "Catch regressions early",
        "trigger": "CI run",
        "success_outcome": "Tests pass",
    },
    "scope": {
        "in_scope": ["Unit testing"],
        "out_of_scope": ["Production use"],
    },
    "preconditions": ["Python 3 available"],
    "business_rules": [
        {"id": "BR-001", "rule": "A rule", "applies_when": "always", "outcome": "good"}
    ],
    "decision_tables": [
        {
            "id": "DT-001",
            "name": "Decision table",
            "inputs": ["x"],
            "rows": [],
            "default_outcome": "none",
        }
    ],
    "interfaces": [
        {
            "id": "IF-001",
            "kind": "cli",
            "name": "my-cli",
            "operation": "run",
            "inputs": [],
            "outputs": [{"condition": "ok", "status": "exit 0", "body": "result"}],
            "errors": [],
        }
    ],
    "acceptance_scenarios": [
        {
            "id": "AC-001",
            "title": "Works",
            "given": ["given"],
            "when": ["when"],
            "then": ["then"],
            "traces_to": ["BR-001", "DT-001", "IF-001"],
        }
    ],
    "decision_log": [
        {
            "id": "D-001",
            "topic": "scope",
            "decision": "Keep it small",
            "source": "user",
            "evidence_ids": ["EV-001"],
            "rationale": "YAGNI",
        }
    ],
    "codebase_evidence": [
        {
            "id": "EV-001",
            "path": "src/main.go",
            "symbol": "main",
            "location": "src/main.go:1",
            "finding": "Entry point",
            "verified": True,
        }
    ],
    "read_models": [],
    "process_steps": [],
    "integration_contracts": [],
    "state_changes": [],
    "side_effects": [],
    "open_questions": [],
    "assumptions_requiring_confirmation": [],
    "blockers": [],
    "depends_on": [],
}


class TestWarnPlaceholders(unittest.TestCase):
    def test_detects_tbd_in_summary(self):
        data = {"summary": "tbd"}
        warnings = vp.warn_placeholders(data)
        self.assertTrue(any("tbd" in w.lower() for w in warnings))

    def test_detects_todo_case_insensitive(self):
        data = {"summary": "TODO: fill this in"}
        warnings = vp.warn_placeholders(data)
        self.assertTrue(len(warnings) > 0)

    def test_no_warning_for_clean_string(self):
        data = {"summary": "A clean implementation summary."}
        warnings = vp.warn_placeholders(data)
        self.assertEqual(warnings, [])

    def test_nested_string_is_detected(self):
        data = {"stakeholder_intent": {"goal": "unknown at this time"}}
        warnings = vp.warn_placeholders(data)
        self.assertTrue(len(warnings) > 0)

    def test_warning_line_prefix(self):
        data = {"summary": "tbd"}
        warnings = vp.warn_placeholders(data)
        self.assertTrue(all(w.startswith("WARNING:") for w in warnings))


class TestWarnCrossReferences(unittest.TestCase):
    def test_unreferenced_evidence_emits_warning(self):
        data = {
            "codebase_evidence": [{"id": "EV-001", "path": "foo.py"}],
            "decision_log": [{"id": "D-001", "evidence_ids": []}],
            "acceptance_scenarios": [],
            "business_rules": [],
        }
        warnings = vp.warn_cross_references(data)
        self.assertTrue(any("EV-001" in w for w in warnings))

    def test_referenced_evidence_no_warning(self):
        data = {
            "codebase_evidence": [{"id": "EV-001", "path": "foo.py"}],
            "decision_log": [{"id": "D-001", "evidence_ids": ["EV-001"]}],
            "acceptance_scenarios": [],
            "business_rules": [],
        }
        warnings = vp.warn_cross_references(data)
        self.assertFalse(any("EV-001" in w for w in warnings))

    def test_untraced_business_rule_emits_warning(self):
        data = {
            "codebase_evidence": [],
            "decision_log": [],
            "acceptance_scenarios": [],
            "business_rules": [{"id": "BR-001", "rule": "x"}],
        }
        warnings = vp.warn_cross_references(data)
        self.assertTrue(any("BR-001" in w for w in warnings))

    def test_traced_business_rule_no_warning(self):
        data = {
            "codebase_evidence": [],
            "decision_log": [],
            "acceptance_scenarios": [{"id": "AC-001", "traces_to": ["BR-001"]}],
            "business_rules": [{"id": "BR-001", "rule": "x"}],
        }
        warnings = vp.warn_cross_references(data)
        self.assertFalse(any("BR-001" in w for w in warnings))


class TestCLIBehavior(unittest.TestCase):
    """Integration tests running the script as subprocess — covers AC-004, AC-005, AC-006."""

    def test_ac004_placeholder_warning_at_scope_stage(self):
        """Placeholder word in summary emits WARNING at scope stage with exit 0."""
        data = dict(_MINIMAL_SLICE, summary="tbd")
        result = _run_stage(data, "scope")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("WARNING", result.stdout)
        self.assertIn("tbd", result.stdout)

    def test_ac004_warning_names_field_path(self):
        data = dict(_MINIMAL_SLICE, summary="tbd")
        result = _run_stage(data, "scope")
        self.assertIn("summary", result.stdout)

    def test_ac005_no_cross_ref_warnings_at_50_percent(self):
        """Cross-reference problems must not appear at 50_percent stage."""
        data = dict(
            _MINIMAL_SLICE,
            codebase_evidence=[{"id": "EV-001", "path": "x.py"}],
            decision_log=[
                {
                    "id": "D-001",
                    "topic": "t",
                    "decision": "d",
                    "source": "user",
                    "evidence_ids": [],
                    "rationale": "r",
                }
            ],
            business_rules=[
                {"id": "BR-999", "rule": "orphan", "applies_when": "x", "outcome": "y"}
            ],
            acceptance_scenarios=[
                {
                    "id": "AC-001",
                    "title": "t",
                    "given": ["g"],
                    "when": ["w"],
                    "then": ["t"],
                    "traces_to": [],
                }
            ],
        )
        result = _run_stage(data, "50_percent")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("EV-001", result.stdout)
        self.assertNotIn("BR-999", result.stdout)

    def test_ac005_cross_ref_warnings_appear_at_full_draft(self):
        """Unreferenced evidence and untraced rule show up at full_draft."""
        data = dict(
            _MINIMAL_SLICE,
            codebase_evidence=[{"id": "EV-001", "path": "x.py", "symbol": None, "location": "x.py:1", "finding": "found", "verified": True}],
            decision_log=[
                {
                    "id": "D-001",
                    "topic": "t",
                    "decision": "d",
                    "source": "user",
                    "evidence_ids": [],
                    "rationale": "r",
                }
            ],
            business_rules=[
                {"id": "BR-999", "rule": "orphan rule", "applies_when": "x", "outcome": "y"}
            ],
            acceptance_scenarios=[
                {
                    "id": "AC-001",
                    "title": "t",
                    "given": ["g"],
                    "when": ["w"],
                    "then": ["t"],
                    "traces_to": [],
                }
            ],
        )
        result = _run_stage(data, "full_draft")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("WARNING", result.stdout)
        self.assertIn("EV-001", result.stdout)
        self.assertIn("BR-999", result.stdout)

    def test_ac006_clean_slice_no_warnings(self):
        """A clean slice emits no WARNING lines at any partial stage."""
        for stage in ("stakeholder_intent", "scope", "preconditions", "business_rules", "50_percent", "full_draft"):
            with self.subTest(stage=stage):
                result = _run_stage(_MINIMAL_SLICE, stage)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertNotIn("WARNING", result.stdout, f"Unexpected WARNING at stage {stage}")

    def test_exit_0_despite_placeholder_warning(self):
        """Stage passes (exit 0) even when placeholder warnings are emitted."""
        data = dict(_MINIMAL_SLICE, summary="TODO: fill later")
        result = _run_stage(data, "stakeholder_intent")
        self.assertEqual(result.returncode, 0)
        self.assertIn("WARNING", result.stdout)


if __name__ == "__main__":
    unittest.main()
