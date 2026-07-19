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
from validate_slice import semantic_errors

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
        """--append keeps the old accumulate-and-number behavior."""
        frag1 = json.dumps([{"rule": "R1", "applies_when": "a", "outcome": "x"}])
        frag2 = json.dumps([{"rule": "R2", "applies_when": "b", "outcome": "y"}])
        run_slice("set", str(self.path), "business_rules", frag1)
        run_slice("set", str(self.path), "business_rules", "--append", frag2)
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


# ---------------------------------------------------------------------------
# F1: set replaces array by default; --append keeps old behavior
# ---------------------------------------------------------------------------

class TestSetReplace(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(self.path))

    def tearDown(self):
        self.tmp.cleanup()

    def test_ac_f1_1_set_replaces_by_default(self):
        frag1 = json.dumps([
            {"rule": "A", "applies_when": "x", "outcome": "ok"},
            {"rule": "B", "applies_when": "y", "outcome": "ok"},
        ])
        frag2 = json.dumps([
            {"rule": "C", "applies_when": "z", "outcome": "ok"},
            {"rule": "D", "applies_when": "w", "outcome": "ok"},
        ])
        run_slice("set", str(self.path), "business_rules", frag1)
        rc, stdout, stderr = run_slice("set", str(self.path), "business_rules", frag2)
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        ids = [item["id"] for item in data["business_rules"]]
        self.assertEqual(ids, ["BR-001", "BR-002"])
        self.assertIn("replaced", stdout)

    def test_ac_f1_2_append_flag_accumulates(self):
        frag1 = json.dumps([
            {"rule": "A", "applies_when": "x", "outcome": "ok"},
            {"rule": "B", "applies_when": "y", "outcome": "ok"},
        ])
        frag2 = json.dumps([
            {"rule": "C", "applies_when": "z", "outcome": "ok"},
            {"rule": "D", "applies_when": "w", "outcome": "ok"},
        ])
        run_slice("set", str(self.path), "business_rules", frag1)
        rc, stdout, stderr = run_slice("set", str(self.path), "business_rules", "--append", frag2)
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        ids = [item["id"] for item in data["business_rules"]]
        self.assertEqual(ids, ["BR-001", "BR-002", "BR-003", "BR-004"])
        self.assertIn("appended", stdout)
        self.assertIn("total now 4", stdout)

    def test_ac_f1_3_replace_breaks_cross_ref_fails(self):
        # Seed BR-001 and BR-002
        frag_br = json.dumps([
            {"rule": "A", "applies_when": "x", "outcome": "ok"},
            {"rule": "B", "applies_when": "y", "outcome": "ok"},
        ])
        run_slice("set", str(self.path), "business_rules", frag_br)
        sc_frag = json.dumps([{
            "target": "Order", "precondition": "exists",
            "changes": [{"field": "status", "operation": "set", "value_source": "DONE"}],
        }])
        run_slice("set", str(self.path), "state_changes", sc_frag)
        # Add acceptance scenario referencing BR-002
        ac_frag = json.dumps([{
            "title": "S", "given": ["g"], "when": ["w"], "then": ["t"],
            "traces_to": ["BR-002", "SC-001"],
        }])
        run_slice("set", str(self.path), "acceptance_scenarios", ac_frag)
        # Replace business_rules with only one item — BR-002 vanishes
        replace_frag = json.dumps([{"rule": "C", "applies_when": "z", "outcome": "ok"}])
        rc, stdout, stderr = run_slice("set", str(self.path), "business_rules", replace_frag)
        self.assertNotEqual(rc, 0)
        self.assertIn("BR-002", stderr)
        self.assertIn("$.acceptance_scenarios[", stderr)
        self.assertIn("].traces_to[", stderr)

    def test_ac_f1_4_scalar_set_unaffected(self):
        rc, stdout, stderr = run_slice("set", str(self.path), "risk", '"low"')
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        self.assertEqual(data["risk"], "low")

    def test_replace_success_message_format(self):
        frag = json.dumps([{"rule": "A", "applies_when": "x", "outcome": "ok"}])
        _, stdout, _ = run_slice("set", str(self.path), "business_rules", frag)
        self.assertIn("replaced with 1 item(s)", stdout)
        self.assertIn("BR-001", stdout)


# ---------------------------------------------------------------------------
# F2: placeholder detection ignores quoted domain text
# ---------------------------------------------------------------------------

class TestValidatePlaceholder(unittest.TestCase):
    def _ready_doc_with_summary(self, text: str) -> dict:
        doc = _make_full_slice()
        doc["summary"] = text
        return doc

    def test_ac_f2_1_backtick_quoted_passes(self):
        doc = self._ready_doc_with_summary("`Unknown business-rule reference`")
        errs = [e for e in semantic_errors(doc) if "placeholder" in e.lower()]
        self.assertEqual(errs, [], errs)

    def test_ac_f2_1_double_quote_quoted_passes(self):
        doc = self._ready_doc_with_summary('"Unknown business-rule reference"')
        errs = [e for e in semantic_errors(doc) if "placeholder" in e.lower()]
        self.assertEqual(errs, [], errs)

    def test_ac_f2_2_bare_unknown_fails(self):
        doc = self._ready_doc_with_summary("Unknown value here")
        errs = [e for e in semantic_errors(doc) if "placeholder" in e.lower()]
        self.assertGreater(len(errs), 0)

    def test_ac_f2_2_bare_tbd_fails(self):
        doc = self._ready_doc_with_summary("TBD")
        errs = [e for e in semantic_errors(doc) if "placeholder" in e.lower()]
        self.assertGreater(len(errs), 0)

    def test_ac_f2_2_bare_fixme_fails(self):
        doc = self._ready_doc_with_summary("fixme this part")
        errs = [e for e in semantic_errors(doc) if "placeholder" in e.lower()]
        self.assertGreater(len(errs), 0)

    def test_ac_f2_3_error_contains_token_and_path(self):
        doc = self._ready_doc_with_summary("Unknown value here")
        errs = [e for e in semantic_errors(doc) if "placeholder" in e.lower()]
        self.assertTrue(any("unknown" in e.lower() for e in errs))
        self.assertTrue(any("$." in e for e in errs))


# ---------------------------------------------------------------------------
# F3: init error message names exact recovery command
# ---------------------------------------------------------------------------

class TestInitErrorMessage(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"

    def tearDown(self):
        self.tmp.cleanup()

    def test_ac_f3_1_error_names_exact_recovery_command(self):
        run_slice("init", "query", "--out", str(self.path))
        rc, stdout, stderr = run_slice("init", "query", "--out", str(self.path))
        self.assertEqual(rc, 1)
        self.assertIn("init --force query", stderr)

    def test_f3_includes_type_in_recovery_command(self):
        run_slice("init", "command", "--out", str(self.path))
        _, _, stderr = run_slice("init", "command", "--out", str(self.path))
        self.assertIn("init --force command", stderr)


# ---------------------------------------------------------------------------
# F4: set-scalar for bare-string scalar sections
# ---------------------------------------------------------------------------

class TestSetScalar(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(self.path))

    def tearDown(self):
        self.tmp.cleanup()

    def test_ac_f4_1_set_scalar_risk(self):
        rc, _, stderr = run_slice("set-scalar", str(self.path), "risk", "low")
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        self.assertEqual(data["risk"], "low")

    def test_ac_f4_1_set_scalar_matches_json_quoted_form(self):
        path2 = self.path.parent / "s2.json"
        run_slice("init", "command", "--out", str(path2))
        run_slice("set-scalar", str(self.path), "risk", "low")
        run_slice("set", str(path2), "risk", '"low"')
        self.assertEqual(read_json(self.path)["risk"], read_json(path2)["risk"])

    def test_ac_f4_2_set_scalar_wrong_section_exits_nonzero(self):
        rc, _, stderr = run_slice("set-scalar", str(self.path), "business_rules", "foo")
        self.assertNotEqual(rc, 0)
        for name in ("title", "summary", "risk", "readiness"):
            self.assertIn(name, stderr, f"'{name}' missing from stderr: {stderr}")

    def test_ac_f4_3_set_scalar_readiness_ready(self):
        rc, _, stderr = run_slice("set-scalar", str(self.path), "readiness", "ready")
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        self.assertEqual(data["readiness"], "ready")

    def test_f4_set_scalar_title(self):
        rc, _, stderr = run_slice("set-scalar", str(self.path), "title", "My Feature")
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        self.assertEqual(data["title"], "My Feature")

    def test_f4_set_scalar_summary(self):
        rc, _, stderr = run_slice("set-scalar", str(self.path), "summary", "A brief summary.")
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        self.assertEqual(data["summary"], "A brief summary.")


# ---------------------------------------------------------------------------
# F5: remove an item by ID with dangling-reference guard
# ---------------------------------------------------------------------------

class TestRemove(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.path = self.dir / "s.json"
        run_slice("init", "command", "--out", str(self.path))
        frag = json.dumps([
            {"rule": "A", "applies_when": "x", "outcome": "ok"},
            {"rule": "B", "applies_when": "y", "outcome": "ok"},
            {"rule": "C", "applies_when": "z", "outcome": "ok"},
        ])
        run_slice("set", str(self.path), "business_rules", frag)

    def tearDown(self):
        self.tmp.cleanup()

    def test_ac_f5_1_remove_middle_item_renumbers(self):
        rc, stdout, stderr = run_slice("remove", str(self.path), "business_rules", "BR-002")
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        ids = [item["id"] for item in data["business_rules"]]
        self.assertEqual(ids, ["BR-001", "BR-002"])
        rules = [item["rule"] for item in data["business_rules"]]
        self.assertEqual(rules, ["A", "C"])  # order preserved, B removed
        self.assertIn("renumbered 2 item(s)", stdout)

    def test_ac_f5_2_remove_referenced_item_exits_nonzero(self):
        sc_frag = json.dumps([{
            "target": "O", "precondition": "e",
            "changes": [{"field": "f", "operation": "set", "value_source": "v"}],
        }])
        run_slice("set", str(self.path), "state_changes", sc_frag)
        ac_frag = json.dumps([{
            "title": "S", "given": ["g"], "when": ["w"], "then": ["t"],
            "traces_to": ["BR-002", "SC-001"],
        }])
        run_slice("set", str(self.path), "acceptance_scenarios", ac_frag)
        data_before = read_json(self.path)
        rc, _, stderr = run_slice("remove", str(self.path), "business_rules", "BR-002")
        self.assertNotEqual(rc, 0)
        self.assertIn("BR-002", stderr)
        self.assertIn("$.acceptance_scenarios[", stderr)
        self.assertIn("].traces_to[", stderr)
        # File must not be modified
        data_after = read_json(self.path)
        self.assertEqual(data_before["business_rules"], data_after["business_rules"])

    def test_ac_f5_3_remove_renumbers_updates_cross_refs(self):
        # Add evidence that references BR-002 via decision_log rule_ids — not directly
        # Instead test: decision_tables row referencing BR-003 is updated when BR-002 removed
        sc_frag = json.dumps([{
            "target": "O", "precondition": "e",
            "changes": [{"field": "f", "operation": "set", "value_source": "v"}],
        }])
        run_slice("set", str(self.path), "state_changes", sc_frag)
        ac_frag = json.dumps([{
            "title": "S", "given": ["g"], "when": ["w"], "then": ["t"],
            "traces_to": ["BR-003", "SC-001"],
        }])
        run_slice("set", str(self.path), "acceptance_scenarios", ac_frag)
        # Remove BR-002 — BR-003 must become BR-002 and the trace must update
        rc, _, stderr = run_slice("remove", str(self.path), "business_rules", "BR-002")
        self.assertEqual(rc, 0, stderr)
        data = read_json(self.path)
        # BR-003 is now BR-002 in business_rules
        ids = [item["id"] for item in data["business_rules"]]
        self.assertIn("BR-002", ids)
        self.assertNotIn("BR-003", ids)
        # acceptance_scenarios trace must be updated to the new ID
        traces = data["acceptance_scenarios"][0]["traces_to"]
        self.assertNotIn("BR-003", traces)
        self.assertIn("BR-002", traces)

    def test_ac_f5_4_remove_nonexistent_id_exits_nonzero(self):
        data_before = read_json(self.path)
        rc, _, stderr = run_slice("remove", str(self.path), "business_rules", "BR-999")
        self.assertNotEqual(rc, 0)
        self.assertIn("BR-999", stderr)
        data_after = read_json(self.path)
        self.assertEqual(data_before["business_rules"], data_after["business_rules"])

    def test_f5_remove_unknown_section_exits_nonzero(self):
        rc, _, stderr = run_slice("remove", str(self.path), "nonexistent_section", "X-001")
        self.assertNotEqual(rc, 0)

    def test_f5_remove_last_required_item_refuses(self):
        """Removing the sole item from a minItems:1 section must be refused."""
        tmp = tempfile.TemporaryDirectory()
        path = Path(tmp.name) / "s.json"
        run_slice("init", "command", "--out", str(path))
        frag = json.dumps([{"rule": "Only", "applies_when": "x", "outcome": "ok"}])
        run_slice("set", str(path), "business_rules", frag)
        data_before = read_json(path)
        rc, _, stderr = run_slice("remove", str(path), "business_rules", "BR-001")
        self.assertNotEqual(rc, 0, "should refuse: business_rules has minItems 1")
        data_after = read_json(path)
        self.assertEqual(data_before["business_rules"], data_after["business_rules"])
        tmp.cleanup()

    def test_f5_finalize_passes_after_remove(self):
        """AC-F5-3: finalize must pass on a complete slice after removing an unreferenced item."""
        tmp = tempfile.TemporaryDirectory()
        path = Path(tmp.name) / "s.json"
        # Start from the full valid example slice
        import shutil
        shutil.copy(EXAMPLE_SLICE, path)
        # Append an extra unreferenced business rule
        extra = json.dumps([{"rule": "Extra", "applies_when": "never", "outcome": "none"}])
        run_slice("set", str(path), "business_rules", "--append", extra)
        data_mid = read_json(path)
        extra_id = data_mid["business_rules"][-1]["id"]
        # Remove the extra item
        rc, _, stderr = run_slice("remove", str(path), "business_rules", extra_id)
        self.assertEqual(rc, 0, stderr)
        # Full finalize (normalize + validate) must pass
        rc2, stdout2, stderr2 = run_slice("finalize", str(path))
        self.assertEqual(rc2, 0, stderr2)
        tmp.cleanup()


if __name__ == "__main__":
    unittest.main()
