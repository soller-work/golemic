#!/usr/bin/env python3
"""
Generate a minimal valid JSON skeleton from schema.json.

This tool creates a valid-but-incomplete slice.json with all required fields
and correct types, preventing agents from inventing wrong structures.

Usage:
  python3 schema-scaffold.py schema.json --slice-type command --output slice.json

This generates a skeleton with:
  - All required fields present
  - Correct types (strings as "", arrays as [], etc.)
  - Placeholder values (FILL_IN, PLACEHOLDER)
  - Proper nesting structure

The agent then fills in the blanks and validates incrementally.
"""

import json
import sys
import argparse


def get_default_value(prop_schema):
    """Return a default/placeholder value for a schema property."""
    if prop_schema.get('type') == 'object':
        return {}
    elif prop_schema.get('type') == 'array':
        return []
    elif prop_schema.get('type') == 'string':
        return "FILL_IN"
    elif prop_schema.get('type') == 'boolean':
        return False
    else:
        return None


def scaffold_business_rules(num=1):
    """Generate placeholder business_rules array."""
    return [
        {
            "id": f"BR-{i:03d}",
            "rule": f"FILL_IN: Business rule {i}",
            "applies_when": "FILL_IN",
            "outcome": "FILL_IN"
        }
        for i in range(1, num + 1)
    ]


def scaffold_decision_tables(num=1):
    """Generate placeholder decision_tables array."""
    return [
        {
            "id": f"DT-{i:03d}",
            "name": "FILL_IN",
            "inputs": ["FILL_IN"],
            "rows": [
                {
                    "when": {"input1": "value1"},
                    "then": {"action": "FILL_IN", "outcome": "FILL_IN"},
                    "rule_ids": ["BR-001"]
                }
            ],
            "default_outcome": "FILL_IN"
        }
        for i in range(1, num + 1)
    ]


def scaffold_interfaces(num=1):
    """Generate placeholder interfaces array."""
    return [
        {
            "id": f"IF-{i:03d}",
            "kind": "internal_api",
            "name": "FILL_IN",
            "operation": "FILL_IN",
            "inputs": [
                {
                    "name": "param1",
                    "type": "string",
                    "required": True,
                    "constraints": ["FILL_IN"],
                    "source": "FILL_IN"
                }
            ],
            "outputs": [
                {
                    "condition": "FILL_IN",
                    "status": "ok",
                    "body": "FILL_IN"
                }
            ],
            "errors": [
                {
                    "condition": "FILL_IN",
                    "code": "ERROR_CODE",
                    "status": "error",
                    "message": "FILL_IN"
                }
            ]
        }
        for i in range(1, num + 1)
    ]


def scaffold_state_changes(num=1):
    """Generate placeholder state_changes array."""
    return [
        {
            "id": f"SC-{i:03d}",
            "target": "FILL_IN",
            "precondition": "FILL_IN",
            "changes": [
                {
                    "field": "FILL_IN",
                    "operation": "set",
                    "value_source": "FILL_IN"
                }
            ]
        }
        for i in range(1, num + 1)
    ]


def scaffold_side_effects(num=1):
    """Generate placeholder side_effects array."""
    return [
        {
            "id": f"SE-{i:03d}",
            "type": "other",
            "description": "FILL_IN",
            "delivery": "synchronous",
            "idempotency": "FILL_IN",
            "failure_policy": "FILL_IN"
        }
        for i in range(1, num + 1)
    ]


def scaffold_acceptance_scenarios(num=2):
    """Generate placeholder acceptance_scenarios array."""
    return [
        {
            "id": f"AC-{i:03d}",
            "title": f"Scenario {i}",
            "given": ["FILL_IN"],
            "when": ["FILL_IN"],
            "then": ["FILL_IN"],
            "traces_to": ["BR-001"]
        }
        for i in range(1, num + 1)
    ]


def scaffold_codebase_evidence(num=1):
    """Generate placeholder codebase_evidence array."""
    return [
        {
            "id": f"EV-{i:03d}",
            "symbol": f"Symbol {i}",
            "location": "file.go:10-20",
            "finding": "FILL_IN",
            "verified": False
        }
        for i in range(1, num + 1)
    ]


def scaffold_decision_log(num=2):
    """Generate placeholder decision_log array."""
    return [
        {
            "id": f"D-{i:03d}",
            "topic": "FILL_IN",
            "decision": "FILL_IN",
            "source": "user",
            "evidence_ids": ["EV-001"],
            "rationale": "FILL_IN"
        }
        for i in range(1, num + 1)
    ]


def scaffold_from_schema(schema_path, slice_type, output_path):
    """Generate minimal valid skeleton JSON."""
    with open(schema_path) as f:
        schema = json.load(f)

    # Build skeleton with required fields and sensible defaults
    scaffold = {
        "schema_version": "1.1.0",
        "slice_type": slice_type,
        "depends_on": [],
        "title": "FILL_IN",
        "summary": "FILL_IN",
        "readiness": "blocked",
        "stakeholder_intent": {
            "actor": "FILL_IN",
            "goal": "FILL_IN",
            "business_value": "FILL_IN",
            "trigger": "FILL_IN",
            "success_outcome": "FILL_IN"
        },
        "scope": {
            "in_scope": ["FILL_IN"],
            "out_of_scope": ["FILL_IN"]
        },
        "preconditions": ["FILL_IN"],
        "business_rules": scaffold_business_rules(2),
        "decision_tables": scaffold_decision_tables(1),
        "interfaces": scaffold_interfaces(1),
        "read_models": [],
        "process_steps": [],
        "integration_contracts": [],
        "state_changes": scaffold_state_changes(1),
        "side_effects": scaffold_side_effects(1),
        "security": {
            "authentication": "FILL_IN",
            "authorization_rules": ["FILL_IN"],
            "data_classification": "FILL_IN",
            "audit_requirements": ["FILL_IN"]
        },
        "implementation_context": {
            "target_modules": ["FILL_IN"],
            "integration_points": ["FILL_IN"],
            "architecture_constraints": ["FILL_IN"],
            "allowed_changes": ["FILL_IN"],
            "forbidden_changes": ["FILL_IN"],
            "dependencies": ["FILL_IN"],
            "migration_strategy": "FILL_IN",
            "consistency_and_concurrency": "FILL_IN"
        },
        "acceptance_scenarios": scaffold_acceptance_scenarios(2),
        "quality": {
            "required_test_levels": ["unit"],
            "test_cases": ["FILL_IN"],
            "quality_commands": ["FILL_IN"],
            "non_functional_requirements": ["FILL_IN"],
            "definition_of_done": ["FILL_IN"]
        },
        "decision_log": scaffold_decision_log(2),
        "codebase_evidence": scaffold_codebase_evidence(2),
        "open_questions": [],
        "assumptions_requiring_confirmation": [],
        "blockers": []
    }

    # Write to file with nice formatting
    with open(output_path, 'w') as f:
        json.dump(scaffold, f, indent=2)

    print(f"✅ Scaffold created: {output_path}")
    print(f"\nNext steps:")
    print(f"  1. Replace all 'FILL_IN' placeholders with actual values")
    print(f"  2. Customize business_rules, interfaces, acceptance_scenarios counts")
    print(f"  3. Run: python3 scripts/validate-slice-partial.py {output_path} --stage stakeholder_intent")
    print(f"  4. Once all sections filled, run: python3 scripts/validate_slice.py schema.json {output_path}")


if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description='Generate a minimal valid slice.json skeleton from schema.json'
    )
    parser.add_argument('schema', help='Path to schema.json')
    parser.add_argument(
        '--slice-type',
        required=True,
        choices=['command', 'query', 'process', 'integration'],
        help='Type of slice to scaffold'
    )
    parser.add_argument(
        '--output',
        default='slice.json',
        help='Output file path (default: slice.json)'
    )

    args = parser.parse_args()

    try:
        scaffold_from_schema(args.schema, args.slice_type, args.output)
    except FileNotFoundError as e:
        print(f"❌ Error: {e}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"❌ Unexpected error: {e}", file=sys.stderr)
        sys.exit(1)
