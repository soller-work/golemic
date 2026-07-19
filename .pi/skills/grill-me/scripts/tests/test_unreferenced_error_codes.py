"""Tests for UNREFERENCED_ERROR_CODE semantic check (issue #64)."""

import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))
from validate_slice import semantic_errors


def _doc_with_code(code: str, *, business_rules=None, decision_tables=None, readiness="blocked"):
    doc = {
        "readiness": readiness,
        "blockers": [{"kind": "question", "text": "Needed?"}] if readiness == "blocked" else [],
        "interfaces": [
            {
                "id": "IF-001",
                "errors": [{"code": code, "condition": "When it fails"}],
            }
        ],
        "business_rules": business_rules if business_rules is not None else [],
        "decision_tables": decision_tables if decision_tables is not None else [],
    }
    return doc


class TestUnreferencedErrorCodes:
    def test_code_referenced_by_business_rule_outcome_passes(self):
        """AC-001: code verbatim in business_rules outcome → no finding."""
        doc = _doc_with_code(
            "ORDER_NOT_OWNED",
            business_rules=[
                {"id": "BR-1", "rule": "Owner check.", "outcome": "Emit ORDER_NOT_OWNED if not owned."}
            ],
        )
        findings = semantic_errors(doc)
        assert not any("ORDER_NOT_OWNED" in f and "Unreferenced" in f for f in findings)

    def test_code_referenced_by_business_rule_rule_field_passes(self):
        """Code verbatim in business_rules rule text → no finding."""
        doc = _doc_with_code(
            "ORDER_NOT_OWNED",
            business_rules=[
                {"id": "BR-1", "rule": "If ORDER_NOT_OWNED applies, reject.", "outcome": "Reject."}
            ],
        )
        findings = semantic_errors(doc)
        assert not any("ORDER_NOT_OWNED" in f and "Unreferenced" in f for f in findings)

    def test_code_referenced_by_decision_table_row_then_passes(self):
        """AC-002: code verbatim in decision_tables row then value → no finding."""
        doc = _doc_with_code(
            "ORDER_ALREADY_SHIPPED",
            decision_tables=[
                {
                    "id": "DT-1",
                    "rows": [
                        {
                            "when": {"shipped": "true"},
                            "then": {"result": "emit ORDER_ALREADY_SHIPPED"},
                        }
                    ],
                }
            ],
        )
        findings = semantic_errors(doc)
        assert not any("ORDER_ALREADY_SHIPPED" in f and "Unreferenced" in f for f in findings)

    def test_unreferenced_code_emits_finding(self):
        """AC-003: code not in any reference text → UNREFERENCED_ERROR_CODE finding."""
        doc = _doc_with_code(
            "UNKNOWN_CODE",
            business_rules=[{"id": "BR-1", "rule": "Some rule.", "outcome": "Some outcome."}],
        )
        findings = semantic_errors(doc)
        assert any("Unreferenced error code UNKNOWN_CODE" in f for f in findings)
        assert any("$.interfaces[0].errors[0].code" in f for f in findings)

    def test_unreferenced_finding_includes_correct_path(self):
        """Finding message includes exact JSON path."""
        doc = _doc_with_code("MISSING_CODE")
        findings = semantic_errors(doc)
        assert any(
            "Unreferenced error code MISSING_CODE at $.interfaces[0].errors[0].code" in f
            for f in findings
        )

    def test_paraphrase_does_not_satisfy_check(self):
        """AC-004: lowercase paraphrase without verbatim token → UNREFERENCED_ERROR_CODE."""
        doc = _doc_with_code(
            "ORDER_NOT_OWNED",
            business_rules=[{"id": "BR-1", "rule": "not owned case.", "outcome": "Reject."}],
        )
        findings = semantic_errors(doc)
        assert any("Unreferenced error code ORDER_NOT_OWNED" in f for f in findings)

    def test_empty_interfaces_passes_vacuously(self):
        """AC-005: empty interfaces array → no UNREFERENCED_ERROR_CODE findings."""
        doc = {
            "readiness": "blocked",
            "blockers": [{"kind": "question", "text": "Needed?"}],
            "interfaces": [],
            "business_rules": [],
            "decision_tables": [],
        }
        findings = semantic_errors(doc)
        assert not any("Unreferenced error code" in f for f in findings)

    def test_missing_interfaces_passes_vacuously(self):
        """No interfaces key → no UNREFERENCED_ERROR_CODE findings."""
        doc = {
            "readiness": "blocked",
            "blockers": [{"kind": "question", "text": "Needed?"}],
        }
        findings = semantic_errors(doc)
        assert not any("Unreferenced error code" in f for f in findings)

    def test_blocked_slice_with_unreferenced_code_still_fails(self):
        """AC-006: blocked readiness does not exempt from unreferenced-code check."""
        doc = _doc_with_code("ORPHAN_CODE", readiness="blocked")
        findings = semantic_errors(doc)
        assert any("Unreferenced error code ORPHAN_CODE" in f for f in findings)

    def test_multiple_codes_only_unreferenced_ones_fail(self):
        """Only the unreferenced codes produce findings; referenced ones do not."""
        doc = {
            "readiness": "blocked",
            "blockers": [{"kind": "question", "text": "Needed?"}],
            "interfaces": [
                {
                    "id": "IF-1",
                    "errors": [
                        {"code": "GOOD_CODE"},
                        {"code": "BAD_CODE"},
                    ],
                }
            ],
            "business_rules": [
                {"id": "BR-1", "rule": "GOOD_CODE is handled.", "outcome": "OK."}
            ],
            "decision_tables": [],
        }
        findings = semantic_errors(doc)
        assert not any("Unreferenced error code GOOD_CODE" in f for f in findings)
        assert any("Unreferenced error code BAD_CODE" in f for f in findings)

    def test_finding_path_uses_correct_indices(self):
        """JSON path reflects the actual interface and error indices."""
        doc = {
            "readiness": "blocked",
            "blockers": [{"kind": "question", "text": "Needed?"}],
            "interfaces": [
                {"id": "IF-0", "errors": [{"code": "OK_CODE"}]},
                {"id": "IF-1", "errors": [{"code": "GOOD"}, {"code": "ORPHAN"}]},
            ],
            "business_rules": [
                {"id": "BR-1", "rule": "OK_CODE used here.", "outcome": "GOOD is fine."}
            ],
            "decision_tables": [],
        }
        findings = semantic_errors(doc)
        assert any("$.interfaces[1].errors[1].code" in f for f in findings)
        assert not any("$.interfaces[0]" in f for f in findings)
        assert not any("$.interfaces[1].errors[0]" in f for f in findings)
