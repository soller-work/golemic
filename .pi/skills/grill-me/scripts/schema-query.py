#!/usr/bin/env python3
"""
Query schema.json for human-readable field constraints.

This tool makes it easy to understand what values and structures are allowed
for any field in the schema, without reading raw JSON.

Usage:
  python3 schema-query.py schema.json "interfaces[].kind"
  python3 schema-query.py schema.json "business_rules[].applies_when"
  python3 schema-query.py schema.json "decision_tables[].rows[]"

Output shows:
  - Allowed enum values
  - Data type (string, array, object, etc.)
  - Required subfields (for objects)
  - Field description (if present)
"""

import json
import sys
import argparse


def navigate_schema_path(schema, path):
    """
    Navigate a dot-separated path through the schema, handling array notation.
    
    Examples:
      "interfaces" -> schema['properties']['interfaces']
      "interfaces[].kind" -> schema['properties']['interfaces']['items']['properties']['kind']
      "decision_tables[].rows[]" -> schema['properties']['decision_tables']['items']['properties']['rows']['items']
    """
    parts = path.split('.')
    current = schema['properties']

    for part in parts:
        # Handle array notation: "interfaces[]" means "items of interfaces"
        if part.endswith('[]'):
            base = part[:-2]  # Remove []
            if base not in current:
                return None, f"Field not found: {base}"
            current = current[base]
            if 'items' not in current:
                return None, f"Field {base} is not an array type"
            current = current['items']
            # If items has properties, we're inside an object array
            if 'properties' in current:
                current = current['properties']
        else:
            # Regular field navigation
            if part not in current:
                return None, f"Field not found: {part}"
            current = current[part]
            # If it's an object, move to properties for next level
            if isinstance(current, dict) and 'properties' in current:
                current = current['properties']

    return current, None


def print_field_info(field_path, field_schema):
    """Print human-readable information about a schema field."""
    print(f"\n📋 Field: {field_path}")
    print("-" * 60)

    # Type
    if 'type' in field_schema:
        print(f"Type: {field_schema['type']}")

    # Enum values (most useful)
    if 'enum' in field_schema:
        print(f"\n✅ Allowed Values:")
        for val in field_schema['enum']:
            print(f"   - {val}")

    # Required fields for objects
    if 'required' in field_schema:
        print(f"\n📌 Required subfields:")
        for req in field_schema['required']:
            print(f"   - {req}")

    # Const value (only one choice)
    if 'const' in field_schema:
        print(f"\n🔒 Constant value: {field_schema['const']}")

    # Description
    if 'description' in field_schema:
        print(f"\n📝 Description: {field_schema['description']}")

    # Reference ($ref)
    if '$ref' in field_schema:
        print(f"\n🔗 References: {field_schema['$ref']}")

    # Type details for arrays
    if 'type' in field_schema and field_schema['type'] == 'array':
        print(f"   Array element type: {field_schema.get('items', {}).get('type', 'object')}")

    print()


def query_schema(schema_path, field_path):
    """Query the schema and print field information."""
    try:
        with open(schema_path) as f:
            schema = json.load(f)
    except FileNotFoundError:
        print(f"❌ Schema file not found: {schema_path}", file=sys.stderr)
        sys.exit(1)
    except json.JSONDecodeError as e:
        print(f"❌ Invalid JSON in schema: {e}", file=sys.stderr)
        sys.exit(1)

    field_schema, error = navigate_schema_path(schema, field_path)

    if error:
        print(f"❌ Error: {error}", file=sys.stderr)
        sys.exit(1)

    if field_schema is None:
        print(f"❌ Field not found: {field_path}", file=sys.stderr)
        sys.exit(1)

    print_field_info(field_path, field_schema)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description='Query schema.json for human-readable field constraints',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python3 schema-query.py schema.json "slice_type"
  python3 schema-query.py schema.json "interfaces[].kind"
  python3 schema-query.py schema.json "business_rules[]"
  python3 schema-query.py schema.json "decision_tables[].rows[]"
  python3 schema-query.py schema.json "state_changes[].operation"

Use "[]" notation to query array element types.
"""
    )
    parser.add_argument('schema', help='Path to schema.json')
    parser.add_argument('field_path', help='Dot-separated field path (e.g., interfaces[].kind)')

    args = parser.parse_args()

    query_schema(args.schema, args.field_path)
