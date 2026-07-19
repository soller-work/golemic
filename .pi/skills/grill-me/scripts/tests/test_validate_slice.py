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


class TestUnreferencedErrorCode:
    """Unit tests for the interfaces[].errors[].code semantic check (AC-001 through AC-006)."""

    def _make_doc(self, code: str, business_rules=None, decision_tables=None, readiness="ready", blockers=None):
        """Build a minimal dict with interfaces and the given code."""
        if blockers is None:
            blockers = [] if readiness == "ready" else [{"kind": "question", "text": "?"}]
        return {
            "readiness": readiness,
            "blockers": blockers,
            "interfaces": [{"errors": [{"code": code}]}],
            "business_rules": business_rules if business_rules is not None else [],
            "decision_tables": decision_tables if decision_tables is not None else [],
        }

    def _errors(self, doc):
        from validate_slice import semantic_errors
        return semantic_errors(doc)

    def test_code_referenced_in_business_rule_outcome_passes(self):
        """AC-001: error code verbatim in a business_rules outcome emits no finding."""
        doc = self._make_doc(
            "ORDER_NOT_OWNED",
            business_rules=[{"rule": "Only the owner may cancel", "outcome": "Reject non-owners with ORDER_NOT_OWNED"}],
        )
        errs = self._errors(doc)
        assert not any("ORDER_NOT_OWNED" in e and "Unreferenced" in e for e in errs)

    def test_code_referenced_in_decision_table_then_passes(self):
        """AC-002: error code verbatim in decision_tables row then value emits no finding."""
        doc = self._make_doc(
            "ORDER_ALREADY_SHIPPED",
            business_rules=[{"rule": "Only PAID orders can be cancelled", "outcome": "Cancel PAID orders"}],
            decision_tables=[{
                "rows": [{"when": {}, "then": {"result": "reject with ORDER_ALREADY_SHIPPED"}}]
            }],
        )
        errs = self._errors(doc)
        assert not any("ORDER_ALREADY_SHIPPED" in e and "Unreferenced" in e for e in errs)

    def test_unreferenced_code_fails_with_correct_path(self):
        """AC-003: unreferenced error code emits finding with correct JSON path and token."""
        doc = self._make_doc("UNKNOWN_CODE", business_rules=[], decision_tables=[])
        errs = self._errors(doc)
        assert any("Unreferenced error code UNKNOWN_CODE" in e for e in errs)
        assert any("$.interfaces[0].errors[0].code" in e for e in errs)

    def test_paraphrase_does_not_satisfy_check(self):
        """AC-004: lowercase paraphrase without verbatim token yields a finding."""
        doc = self._make_doc(
            "ORDER_NOT_OWNED",
            business_rules=[{"rule": "Only the owner may cancel; reject if not owned", "outcome": "reject if not owned"}],
        )
        errs = self._errors(doc)
        assert any("Unreferenced error code ORDER_NOT_OWNED" in e for e in errs)

    def test_empty_interfaces_passes_vacuously(self):
        """AC-005: empty interfaces array emits no UNREFERENCED_ERROR_CODE findings."""
        from validate_slice import semantic_errors
        doc = {"readiness": "ready", "blockers": [], "interfaces": [], "business_rules": [], "decision_tables": []}
        errs = semantic_errors(doc)
        assert not any("Unreferenced error code" in e for e in errs)

    def test_blocked_slice_still_checked(self):
        """AC-006: blocked slice with unreferenced code still emits a finding."""
        doc = self._make_doc(
            "GHOST_CODE",
            business_rules=[{"rule": "Some rule", "outcome": "Some outcome"}],
            readiness="blocked",
            blockers=[{"kind": "question", "text": "Is this correct?"}],
        )
        errs = self._errors(doc)
        assert any("Unreferenced error code GHOST_CODE" in e for e in errs)

    def test_absent_interfaces_field_is_no_op(self):
        """Documents without an interfaces field (e.g. v2 format) pass the check."""
        from validate_slice import semantic_errors
        doc = {"readiness": "blocked", "blockers": [{"kind": "question", "text": "?"}]}
        errs = semantic_errors(doc)
        assert not any("Unreferenced error code" in e for e in errs)


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
