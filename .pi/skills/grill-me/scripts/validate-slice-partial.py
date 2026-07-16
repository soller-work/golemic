#!/usr/bin/env python3
"""
Validate slice.json at partial completion stages.

This tool allows agents to validate incrementally while filling in a slice.json,
catching errors early rather than at the end when hundreds of lines are already written.

Stages (in order):
  stakeholder_intent  - Just check: actor, goal, business_value, trigger, success_outcome
  scope               - Check intent + scope
  preconditions       - Check intent + scope + preconditions
  business_rules      - Check intent + scope + business_rules
  50_percent          - Check all critical fields filled (intent, scope, rules, interfaces)
  full_draft          - Check all sections have some content (no full validation)
  100_percent         - Run full schema validation (slice_type-specific)

Usage:
  python3 validate-slice-partial.py my-slice.json --stage stakeholder_intent
  python3 validate-slice-partial.py my-slice.json --stage 50_percent
  python3 validate-slice-partial.py my-slice.json --stage 100_percent

Returns exit code 0 if validation passes, 1 if it fails.
"""

import json
import sys
import argparse
import subprocess
from pathlib import Path


STAGES = {
    'stakeholder_intent': {
        'required_fields': ['stakeholder_intent'],
        'description': 'Check stakeholder intent is filled'
    },
    'scope': {
        'required_fields': ['stakeholder_intent', 'scope'],
        'description': 'Check intent + scope'
    },
    'preconditions': {
        'required_fields': ['stakeholder_intent', 'scope', 'preconditions'],
        'description': 'Check intent + scope + preconditions'
    },
    'business_rules': {
        'required_fields': ['stakeholder_intent', 'scope', 'business_rules'],
        'description': 'Check intent + scope + rules'
    },
    '50_percent': {
        'required_fields': [
            'schema_version', 'slice_type', 'slice_id', 'title', 'summary',
            'stakeholder_intent', 'scope', 'business_rules', 'interfaces'
        ],
        'description': 'Check critical fields are populated (≈50% done)'
    },
    'full_draft': {
        'required_fields': [
            'schema_version', 'slice_type', 'slice_id', 'title', 'summary',
            'stakeholder_intent', 'scope', 'preconditions',
            'business_rules', 'decision_tables', 'interfaces',
            'acceptance_scenarios', 'decision_log', 'codebase_evidence'
        ],
        'description': 'Check all major sections have content'
    }
}


def is_empty(value):
    """Check if a value is considered empty for validation purposes."""
    if value is None:
        return True
    if isinstance(value, str):
        return value.strip() == '' or value.startswith('FILL_IN')
    if isinstance(value, list):
        return len(value) == 0
    if isinstance(value, dict):
        return len(value) == 0
    return False


def check_field_filled(slice_data, field_name):
    """Check if a field is non-empty."""
    if field_name not in slice_data:
        return False, f"Missing field: {field_name}"
    
    value = slice_data[field_name]
    
    if is_empty(value):
        return False, f"Field is empty or unfilled: {field_name}"
    
    # For specific field types, check deeper
    if field_name == 'stakeholder_intent':
        required_subfields = ['actor', 'goal', 'business_value', 'trigger', 'success_outcome']
        for subfield in required_subfields:
            if subfield not in value or is_empty(value[subfield]):
                return False, f"Missing stakeholder_intent.{subfield}"
    
    if field_name == 'scope':
        required_subfields = ['in_scope', 'out_of_scope']
        for subfield in required_subfields:
            if subfield not in value:
                return False, f"Missing scope.{subfield}"
    
    return True, None


def validate_partial(slice_path, stage='50_percent'):
    """Validate slice.json at a given completion stage."""
    
    if stage not in STAGES and stage != '100_percent':
        print(f"❌ Unknown stage: {stage}", file=sys.stderr)
        print(f"   Valid stages: {', '.join(list(STAGES.keys()) + ['100_percent'])}", file=sys.stderr)
        sys.exit(1)

    # Load slice
    try:
        with open(slice_path) as f:
            slice_data = json.load(f)
    except FileNotFoundError:
        print(f"❌ Slice file not found: {slice_path}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as e:
        print(f"❌ Invalid JSON in slice: {e}", file=sys.stderr)
        sys.exit(1)

    # Full validation: delegate to validate_slice.py
    if stage == '100_percent':
        script_dir = Path(__file__).parent
        schema_path = script_dir.parent / 'schema.json'
        
        if not schema_path.exists():
            print(f"❌ Schema file not found: {schema_path}", file=sys.stderr)
            sys.exit(1)
        
        print(f"🔍 Running full validation... (this may take a moment)")
        result = subprocess.run([
            sys.executable, str(script_dir / 'validate_slice.py'),
            str(schema_path), slice_path
        ])
        sys.exit(result.returncode)

    # Partial validation
    stage_config = STAGES[stage]
    required_fields = stage_config['required_fields']
    
    print(f"🔍 Stage: {stage}")
    print(f"   Description: {stage_config['description']}")
    print()

    errors = []
    for field in required_fields:
        filled, error = check_field_filled(slice_data, field)
        if filled:
            print(f"   ✅ {field}")
        else:
            print(f"   ❌ {field}")
            errors.append(error)

    print()

    if errors:
        print(f"❌ Validation failed for stage '{stage}':\n")
        for error in errors:
            print(f"   - {error}")
        print()
        print(f"📝 Fix these issues, then re-run:")
        print(f"   python3 validate-slice-partial.py {slice_path} --stage {stage}")
        sys.exit(1)
    else:
        print(f"✅ Stage '{stage}' validation passed!")
        
        # Suggest next steps
        stage_list = list(STAGES.keys())
        current_idx = stage_list.index(stage)
        if current_idx < len(stage_list) - 1:
            next_stage = stage_list[current_idx + 1]
            print()
            print(f"📋 Next steps:")
            print(f"   1. Fill in more fields")
            print(f"   2. Run: python3 validate-slice-partial.py {slice_path} --stage {next_stage}")
        else:
            print()
            print(f"📋 All partial stages complete!")
            print(f"   Run full validation:")
            print(f"   python3 validate-slice-partial.py {slice_path} --stage 100_percent")
        
        sys.exit(0)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description='Validate slice.json at partial completion stages',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=f"""
Stages (in recommended order):
  stakeholder_intent  - Just the intent block
  scope               - Intent + scope
  preconditions       - Intent + scope + preconditions
  business_rules      - Intent + scope + business rules
  50_percent          - Critical sections (roughly 50% complete)
  full_draft          - All major sections have content
  100_percent         - Full schema validation (strictest)

Recommended workflow:
  1. Generate skeleton: python3 schema-scaffold.py schema.json --slice-type command
  2. Fill stakeholder_intent
  3. Validate: python3 validate-slice-partial.py slice.json --stage stakeholder_intent
  4. Fill scope + rules
  5. Validate: python3 validate-slice-partial.py slice.json --stage 50_percent
  6. Continue filling
  7. Final: python3 validate-slice-partial.py slice.json --stage 100_percent
"""
    )
    parser.add_argument('slice', help='Path to slice.json')
    parser.add_argument(
        '--stage',
        default='50_percent',
        choices=list(STAGES.keys()) + ['100_percent'],
        help='Validation stage (default: 50_percent)'
    )

    args = parser.parse_args()
    validate_partial(args.slice, args.stage)
