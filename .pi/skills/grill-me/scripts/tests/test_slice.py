"""Tests for slice.py driver script."""

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS_DIR = Path(__file__).parent.parent
SKILL_DIR = SCRIPTS_DIR.parent
sys.path.insert(0, str(SCRIPTS_DIR))

import slice as sl

SCHEMA_PATH = SKILL_DIR / "schema.json"
EXAMPLE_SLICE = SKILL_DIR / "references" / "example-slice.json"


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def run_slice(*args: str, input_data: str | None = None) -> tuple[int, str, str]:
    result = subprocess.run(
        [sys.executable, str(SCRIPTS_DIR / "slice.py"), *args],
        capture_output=True,
        text=True,
        input=input_data,
    )
    return result.returncode, result.stdout, result.stderr


def write_json(path: Path, data: dict) -> None:
    path.write_text(json.dumps(data, indent=2), encoding="utf-8")


def read_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


# Minimal valid blocked slice fixture for finalize tests
def _make_full_slice() -> dict:
    return json.loads(EXAMPLE_SLICE.read_text(encoding="utf-8"))


# ---------------------------------------------------------------------------
# init: all four slice types produce valid skeletons
# ---------------------------------------------------------------------------

class TestInit(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def _init_and_load(self, slice_type: str) -> dict:
        out = self.dir / f"{slice_type}.json"
        rc, stdout, stderr = run_slice("init", slice_type, "--out", str(out))
        self.assertEqual(rc, 0, stderr)
        return read_json(out)

    def test_init_command_has_required_keys(self):
        data = self._init_and_load("command")
        self.assertEqual(data["schema_version"], "1.1.0")
        self.assertEqual(data["slice_type"], "command")
        self.assertEqual(data["readiness"], "blocked")
        self.assertEqual(data["depends_on"], [])

    def test_init_query_has_required_keys(self):
        data = self._init_and_load("query")
        self.assertEqual(data["slice_type"], "query")
        self.assertIn("read_models", data)

    def test_init_process_has_required_keys(self):
        data = self._init_and_load("process")
        self.assertEqual(data["slice_type"], "process")
        self.assertIn("process_steps", data)

    def test_init_integration_has_required_keys(self):
        data = self._init_and_load("integration")
        self.assertEqual(data["slice_type"], "integration")
        self.assertIn("integration_contracts", data)

    def test_init_skeleton_parses_as_json(self):
        out = self.dir / "s.json"
        rc, _, _ = run_slice("init", "command", "--out", str(out))
        self.assertEqual(rc, 0)
        data = read_json(out)
        self.assertIsInstance(data, dict)

    def test_init_no_fill_in_sentinels(self):
        out = self.dir / "s.json"
        run_slice("init", "command", "--out", str(out))
        text = out.read_text()
        self.assertNotIn("FILL_IN", text)

    def test_init_refuses_overwrite_without_force(self):
        out = self.dir / "s.json"
        run_slice("init", "command", "--out", str(out))
        rc, _, stderr = run_slice("init", "command", "--out", str(out))
        self.assertEqual(rc, 1)
        self.assertIn("--force", stderr)

    def test_init_force_overwrites(self):
        out = self.dir / "s.json"
        run_slice("init", "command", "--out", str(out))
        rc, _, _ = run_slice("init", "query", "--out", str(out), "--force")
        self.assertEqual(rc, 0)
        data = read_json(out)
        self.assertEqual(data["slice_type"], "query")

    def test_init_readiness_is_blocked(self):
        data = self._init_and_load("command")
        self.assertEqual(data["readiness"], "blocked")

    def test_init_all_required_top_level_keys_present(self):
        schema = json.loads(SCHEMA_PATH.read_text())
        required = schema["required"]
        data = self._init_and_load("command")
        for key in required:
            self.assertIn(key, data, f"Missing required key: {key}")


# ---------------------------------------------------------------------------
# set: business_rules ID assignment
# ---------------------------------------------------------------------------

class TestSetBusinessRules(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(self.path))

    def tearDown(self):
        self.tmp.cleanup()

    def test_assigns_br_ids_deterministically(self):
        fragment = json.dumps([
            {"rule": "Rule A", "applies_when": "always", "outcome": "ok"},
            {"rule": "Rule B", "applies_when": "sometimes", "outcome": "rejected"},
            {"rule": "Rule C", "applies_when": "never", "outcome": "ignored"},
        ])
        rc, stdout, stderr = run_slice("set", str(self.path), "business_rules", fragment)
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        ids = [item["id"] for item in data["business_rules"]]
        self.assertEqual(ids, ["BR-001", "BR-002", "BR-003"])

    def test_sequential_sets_continue_numbering(self):
        frag1 = json.dumps([{"rule": "R1", "applies_when": "a", "outcome": "x"}])
        frag2 = json.dumps([{"rule": "R2", "applies_when": "b", "outcome": "y"}])
        run_slice("set", str(self.path), "business_rules", frag1)
        run_slice("set", str(self.path), "business_rules", frag2)
        data = read_json(self.path)
        ids = [item["id"] for item in data["business_rules"]]
        self.assertEqual(ids, ["BR-001", "BR-002"])

    def test_rejects_fragment_with_wrong_id(self):
        fragment = json.dumps([{"id": "BR-999", "rule": "R", "applies_when": "a", "outcome": "x"}])
        rc, _, stderr = run_slice("set", str(self.path), "business_rules", fragment)
        self.assertEqual(rc, 1)
        self.assertIn("mismatch", stderr.lower())

    def test_rejects_fragment_missing_required_subfield(self):
        fragment = json.dumps([{"rule": "R", "applies_when": "a"}])  # missing 'outcome'
        rc, _, stderr = run_slice("set", str(self.path), "business_rules", fragment)
        self.assertEqual(rc, 1)
        self.assertIn("outcome", stderr)

    def test_does_not_write_on_validation_failure(self):
        data_before = read_json(self.path)
        fragment = json.dumps([{"rule": "R"}])  # missing applies_when, outcome
        run_slice("set", str(self.path), "business_rules", fragment)
        data_after = read_json(self.path)
        self.assertEqual(data_before["business_rules"], data_after["business_rules"])


    def test_empty_array_fragment_rejected_min_items(self):
        rc, _, stderr = run_slice("set", str(self.path), "business_rules", "[]")
        self.assertEqual(rc, 1)
        self.assertIn("business_rules", stderr)
        # jsonschema renders minItems as "should be non-empty" or similar
        self.assertTrue(
            "non-empty" in stderr or "minItems" in stderr or "minimum" in stderr.lower(),
            msg=f"Expected a minItems-related error in: {stderr}",
        )

    def test_malformed_item_integer_rejected_cleanly(self):
        rc, stdout, stderr = run_slice("set", str(self.path), "business_rules", "[1]")
        self.assertEqual(rc, 1)
        self.assertNotIn("Traceback", stderr)
        self.assertIn("business_rules[0]", stderr)
        # type name may be 'int' or 'integer' depending on Python version
        self.assertTrue(
            "int" in stderr,
            msg=f"Expected type name in: {stderr}",
        )


# ---------------------------------------------------------------------------
# set: applicability guard (P1)
# ---------------------------------------------------------------------------

class TestSetApplicability(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "query", "--out", str(self.path))

    def tearDown(self):
        self.tmp.cleanup()

    def test_state_changes_rejected_for_query_slice(self):
        fragment = json.dumps([{
            "target": "Order", "precondition": "exists",
            "changes": [{"field": "status", "operation": "set", "value_source": "DONE"}],
        }])
        data_before = read_json(self.path)
        rc, _, stderr = run_slice("set", str(self.path), "state_changes", fragment)
        self.assertNotEqual(rc, 0)
        self.assertIn("state_changes", stderr)
        self.assertIn("query", stderr)
        data_after = read_json(self.path)
        self.assertEqual(data_before["state_changes"], data_after["state_changes"])


# ---------------------------------------------------------------------------
# set: cross-reference validation
# ---------------------------------------------------------------------------

class TestSetCrossRefs(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(self.path))
        # seed a business rule
        frag = json.dumps([{"rule": "R1", "applies_when": "a", "outcome": "x"}])
        run_slice("set", str(self.path), "business_rules", frag)
        # seed a state change so we have a traceable ID
        sc_frag = json.dumps([{"target": "Order", "precondition": "exists",
                               "changes": [{"field": "status", "operation": "set", "value_source": "CANCELLED"}]}])
        run_slice("set", str(self.path), "state_changes", sc_frag)

    def tearDown(self):
        self.tmp.cleanup()

    def test_rejects_traces_to_nonexistent_id(self):
        frag = json.dumps([{
            "title": "Scenario A",
            "given": ["system ready"],
            "when": ["user acts"],
            "then": ["state changes"],
            "traces_to": ["BR-999"],
        }])
        rc, _, stderr = run_slice("set", str(self.path), "acceptance_scenarios", frag)
        self.assertEqual(rc, 1)
        self.assertIn("BR-999", stderr)

    def test_accepts_traces_to_existing_id(self):
        frag = json.dumps([{
            "title": "Scenario A",
            "given": ["system ready"],
            "when": ["user acts"],
            "then": ["state changes"],
            "traces_to": ["BR-001", "SC-001"],
        }])
        rc, stdout, stderr = run_slice("set", str(self.path), "acceptance_scenarios", frag)
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        self.assertEqual(len(data["acceptance_scenarios"]), 1)


# ---------------------------------------------------------------------------
# finalize: readiness enforcement
# ---------------------------------------------------------------------------

class TestFinalize(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_flips_ready_to_blocked_when_open_questions_nonempty(self):
        data = _make_full_slice()
        data["readiness"] = "ready"
        data["open_questions"] = ["What about edge case X?"]
        path = self.dir / "s.json"
        write_json(path, data)
        rc, stdout, stderr = run_slice("finalize", str(path))
        self.assertNotEqual(rc, 0)
        result = read_json(path)
        self.assertEqual(result["readiness"], "blocked")
        combined = stdout + stderr
        self.assertIn("warning", combined.lower())

    def test_finalize_rejects_incomplete_slice(self):
        path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(path))
        rc, _, stderr = run_slice("finalize", str(path))
        self.assertNotEqual(rc, 0)

    def test_finalize_succeeds_on_full_example(self):
        data = _make_full_slice()
        path = self.dir / "s.json"
        write_json(path, data)
        rc, stdout, stderr = run_slice("finalize", str(path))
        self.assertEqual(rc, 0, stderr)
        self.assertIn("valid", stdout + stderr)

    def test_finalize_sorts_ids(self):
        data = _make_full_slice()
        # Swap first two business rules to create unsorted order
        brs = data.get("business_rules", [])
        if len(brs) >= 2:
            data["business_rules"] = [brs[1], brs[0]] + brs[2:]
        path = self.dir / "s.json"
        write_json(path, data)
        run_slice("finalize", str(path))
        result = read_json(path)
        ids = [item["id"] for item in result["business_rules"]]
        self.assertEqual(ids, sorted(ids))


# ---------------------------------------------------------------------------
# plan: basic smoke
# ---------------------------------------------------------------------------

class TestPlan(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(self.path))

    def tearDown(self):
        self.tmp.cleanup()

    def test_plan_exits_zero(self):
        rc, _, _ = run_slice("plan", str(self.path))
        self.assertEqual(rc, 0)

    def test_plan_shows_slice_type(self):
        _, stdout, _ = run_slice("plan", str(self.path))
        self.assertIn("command", stdout)

    def test_plan_shows_readiness(self):
        _, stdout, _ = run_slice("plan", str(self.path))
        self.assertIn("blocked", stdout)

    def test_plan_shows_sections(self):
        _, stdout, _ = run_slice("plan", str(self.path))
        self.assertIn("business_rules", stdout)
        self.assertIn("acceptance_scenarios", stdout)

    def test_plan_query_omits_state_changes(self):
        path = self.dir / "q.json"
        run_slice("init", "query", "--out", str(path))
        _, stdout, _ = run_slice("plan", str(path))
        self.assertNotIn("state_changes", stdout)


# ---------------------------------------------------------------------------
# status: basic smoke
# ---------------------------------------------------------------------------

class TestStatus(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(self.path))

    def tearDown(self):
        self.tmp.cleanup()

    def test_status_exits_zero(self):
        rc, _, _ = run_slice("status", str(self.path))
        self.assertEqual(rc, 0)

    def test_status_shows_sections(self):
        _, stdout, _ = run_slice("status", str(self.path))
        self.assertIn("business_rules", stdout)


if __name__ == "__main__":
    unittest.main()
