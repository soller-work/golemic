"""Single source of truth for the gattung-specific detail block of a slice.

Every consumer (schema-driven skeleton, semantic validation, issue rendering,
fill plan) derives its per-change_type detail fields from DETAIL_BLOCKS here.
No other module may re-declare which detail field belongs to which change_type.

A detail field is rendered either before (`pre_proof`) or after (`post_proof`)
the Proof-of-Delivery section. The split exists so the feature layout stays
byte-identical to the historical one (Inputs/Outputs/Errors renders after Proof).
"""

from __future__ import annotations

from typing import NamedTuple


class FieldSpec(NamedTuple):
    key: str
    label: str
    kind: str  # "markdown" | "scenarios"
    required: bool
    hint: str


DETAIL_BLOCKS: dict[str, dict[str, list[FieldSpec]]] = {
    "feature": {
        "pre_proof": [
            FieldSpec("behavior", "Behavior", "markdown", True,
                      "String (Markdown): type-specific behavior (mutations/read-model/steps/contract)."),
            FieldSpec("business_rules", "Business Rules", "markdown", False,
                      "String (Markdown): decision logic, constraints."),
            FieldSpec("acceptance_scenarios", "Acceptance Scenarios", "scenarios", False,
                      "Array of strings: 'Given...When...Then...'."),
        ],
        "post_proof": [
            FieldSpec("inputs_outputs_errors", "Inputs / Outputs / Errors", "markdown", True,
                      "String (Markdown): I/O contract, validation, errors."),
        ],
    },
    "bug": {
        "pre_proof": [
            FieldSpec("reproduction", "Reproduction", "markdown", True,
                      "String (Markdown): steps to reproduce, observed vs expected behavior."),
            FieldSpec("root_cause", "Root Cause", "markdown", True,
                      "String (Markdown): hypothesis and suspected location of the defect."),
            FieldSpec("regression_scenarios", "Regression Scenarios", "scenarios", True,
                      "Array of strings: 'Given...When...Then...' that fails today and must pass after the fix."),
        ],
        "post_proof": [],
    },
    "refactoring": {
        "pre_proof": [
            FieldSpec("current_structure", "Current Structure", "markdown", True,
                      "String (Markdown): today's shape / the smell being removed."),
            FieldSpec("target_structure", "Target Structure", "markdown", True,
                      "String (Markdown): the intended structure after the change."),
            FieldSpec("behavior_preservation", "Behavior Preservation", "markdown", True,
                      "String (Markdown): the observable invariant that must stay identical."),
        ],
        "post_proof": [],
    },
}


def pre_proof_fields(change_type: str) -> list[FieldSpec]:
    return DETAIL_BLOCKS.get(change_type, {}).get("pre_proof", [])


def post_proof_fields(change_type: str) -> list[FieldSpec]:
    return DETAIL_BLOCKS.get(change_type, {}).get("post_proof", [])


def detail_fields(change_type: str) -> list[FieldSpec]:
    """All detail fields for a change_type, in fill/render order."""
    return pre_proof_fields(change_type) + post_proof_fields(change_type)


def all_detail_keys() -> set[str]:
    """Every detail field key across all gattungen (the union)."""
    keys: set[str] = set()
    for change_type in DETAIL_BLOCKS:
        keys.update(f.key for f in detail_fields(change_type))
    return keys
