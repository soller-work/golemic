"""Tests for schema-query.py — covers AC-001, AC-002, AC-003."""

import importlib.util
import json
import subprocess
import sys
import unittest
from pathlib import Path

SCRIPTS_DIR = Path(__file__).parent.parent
SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"


def _load_module():
    spec = importlib.util.spec_from_file_location(
        "schema_query", SCRIPTS_DIR / "schema-query.py"
    )
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


sq = _load_module()


def _run(field_path):
    return subprocess.run(
        [sys.executable, str(SCRIPTS_DIR / "schema-query.py"), str(SCHEMA_PATH), field_path],
        capture_output=True,
        text=True,
    )


class TestRefResolution(unittest.TestCase):
    def setUp(self):
        self.schema = json.loads(SCHEMA_PATH.read_text())

    def test_resolve_ref_returns_definition(self):
        node = {"$ref": "#/$defs/nonEmptyString"}
        resolved = sq.resolve_ref(self.schema, node)
        self.assertEqual(resolved.get("type"), "string")

    def test_resolve_ref_passthrough_no_ref(self):
        node = {"type": "integer"}
        self.assertIs(sq.resolve_ref(self.schema, node), node)

    def test_resolve_ref_unknown_def_passthrough(self):
        node = {"$ref": "#/$defs/doesNotExist"}
        result = sq.resolve_ref(self.schema, node)
        self.assertEqual(result, node)


class TestNavigatePath(unittest.TestCase):
    def setUp(self):
        self.schema = json.loads(SCHEMA_PATH.read_text())

    def test_process_steps_array_returns_object_schema(self):
        node, err = sq.navigate_schema_path(self.schema, "process_steps[]")
        self.assertIsNone(err)
        self.assertIsNotNone(node)
        self.assertEqual(node.get("type"), "object")
        self.assertIn("properties", node)

    def test_process_steps_array_has_required_subfields(self):
        node, err = sq.navigate_schema_path(self.schema, "process_steps[]")
        self.assertIsNone(err)
        props = node["properties"]
        for expected in ("id", "order", "name", "terminal", "failure_behavior"):
            self.assertIn(expected, props, f"Expected subfield {expected!r} in process_steps[]")

    def test_leaf_field_slice_type(self):
        node, err = sq.navigate_schema_path(self.schema, "slice_type")
        self.assertIsNone(err)
        self.assertIn("enum", node)

    def test_nested_leaf_interfaces_kind(self):
        node, err = sq.navigate_schema_path(self.schema, "interfaces[].kind")
        self.assertIsNone(err)
        self.assertIn("enum", node)

    def test_unrecognized_field_returns_error(self):
        node, err = sq.navigate_schema_path(self.schema, "process_steps[].nonexistent_field")
        self.assertIsNone(node)
        self.assertIsNotNone(err)
        self.assertIn("nonexistent_field", err)

    def test_unrecognized_top_level_returns_error(self):
        node, err = sq.navigate_schema_path(self.schema, "does_not_exist")
        self.assertIsNone(node)
        self.assertIsNotNone(err)
        self.assertIn("does_not_exist", err)


class TestCLIBehavior(unittest.TestCase):
    """Integration tests that run the script as a subprocess — covers AC-001/AC-002/AC-003."""

    def test_ac001_process_steps_array_exit_0(self):
        result = _run("process_steps[]")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_ac001_process_steps_array_lists_subfields(self):
        result = _run("process_steps[]")
        out = result.stdout
        for field in ("id", "order", "name", "terminal", "failure_behavior"):
            self.assertIn(field, out, f"Expected {field!r} in output")

    def test_ac001_process_steps_array_shows_required_optional(self):
        result = _run("process_steps[]")
        out = result.stdout
        self.assertIn("required", out)

    def test_ac002_slice_type_enum_regression(self):
        result = _run("slice_type")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("command", result.stdout)

    def test_ac002_interfaces_kind_enum_regression(self):
        result = _run("interfaces[].kind")
        self.assertEqual(result.returncode, 0, result.stderr)
        out = result.stdout
        for val in ("cli", "http_api", "ui"):
            self.assertIn(val, out)

    def test_ac003_unrecognized_path_exit_1(self):
        result = _run("process_steps[].nonexistent_field")
        self.assertEqual(result.returncode, 1)

    def test_ac003_unrecognized_path_names_segment_in_stderr(self):
        result = _run("process_steps[].nonexistent_field")
        self.assertIn("nonexistent_field", result.stderr)


if __name__ == "__main__":
    unittest.main()
