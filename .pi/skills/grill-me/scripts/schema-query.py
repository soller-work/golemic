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


def resolve_ref(schema, node):
    """Resolve a $ref against $defs, returning the referenced definition."""
    if not isinstance(node, dict) or '$ref' not in node:
        return node
    ref = node['$ref']
    if ref.startswith('#/$defs/'):
        def_name = ref[len('#/$defs/'):]
        resolved = schema.get('$defs', {}).get(def_name)
        if resolved is not None:
            return resolved
    return node


def navigate_schema_path(schema, path):
    """
    Navigate a dot-separated path through the schema, handling array notation
    and resolving $ref at every step.

    Examples:
      "interfaces" -> schema['properties']['interfaces']
      "interfaces[].kind" -> interfaces items object -> kind field
      "process_steps[]" -> process_steps items object (returns the object schema)
    """
    parts = path.split('.')
    current_props = schema.get('properties', {})

    for i, part in enumerate(parts):
        is_last = (i == len(parts) - 1)

        if part.endswith('[]'):
            base = part[:-2]
            if base not in current_props:
                return None, f"Field not found: {base}"
            array_node = resolve_ref(schema, current_props[base])
            if array_node.get('type') != 'array' or 'items' not in array_node:
                return None, f"Field '{base}' is not an array type"
            items_node = resolve_ref(schema, array_node['items'])
            if is_last:
                return items_node, None
            if 'properties' not in items_node:
                return None, f"Field '{part}' has no subfields to navigate"
            current_props = items_node['properties']
        else:
            if part not in current_props:
                return None, f"Field not found: {part}"
            node = resolve_ref(schema, current_props[part])
            if is_last:
                return node, None
            if 'properties' not in node:
                return None, f"Field '{part}' has no subfields to navigate"
            current_props = node['properties']

    return None, "Empty path"


def print_object_overview(schema, field_path, obj_schema):
    """Print a one-level-deep overview of an object's subfields."""
    print(f"\n📋 Field: {field_path}")
    print("-" * 60)
    print("Type: object\n")
    required = set(obj_schema.get('required', []))
    properties = obj_schema.get('properties', {})
    print("Fields:")
    for fname, fnode in properties.items():
        resolved = resolve_ref(schema, fnode)
        ftype = resolved.get('type', 'object')
        req_str = 'required' if fname in required else 'optional'
        line = f"   {fname} ({ftype}, {req_str})"
        enum_vals = resolved.get('enum', [])
        if enum_vals:
            line += f" — enum: {', '.join(str(v) for v in enum_vals)}"
        if ftype == 'array' and 'items' in resolved:
            item_node = resolve_ref(schema, resolved['items'])
            item_type = item_node.get('type', 'object')
            line += f" — items: {item_type}"
        print(line)
    print()


def print_field_info(schema, field_path, field_schema):
    """Print human-readable information about a schema field."""
    if 'properties' in field_schema:
        print_object_overview(schema, field_path, field_schema)
        return

    print(f"\n📋 Field: {field_path}")
    print("-" * 60)

    if 'type' in field_schema:
        print(f"Type: {field_schema['type']}")

    if 'enum' in field_schema:
        print(f"\n✅ Allowed Values:")
        for val in field_schema['enum']:
            print(f"   - {val}")

    if 'required' in field_schema:
        print(f"\n📌 Required subfields:")
        for req in field_schema['required']:
            print(f"   - {req}")

    if 'const' in field_schema:
        print(f"\n🔒 Constant value: {field_schema['const']}")

    if 'description' in field_schema:
        print(f"\n📝 Description: {field_schema['description']}")

    if field_schema.get('type') == 'array':
        items = field_schema.get('items', {})
        resolved_items = resolve_ref(schema, items)
        print(f"   Array element type: {resolved_items.get('type', 'object')}")

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

    print_field_info(schema, field_path, field_schema)


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
