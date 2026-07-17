"""Unit tests for create_issue.py.

All tests use a fake GhRunner and/or injected validate_fn; no real gh or network
calls are made. Covers AC-001 through AC-010 except AC-008 (documented manual E2E).
"""

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

# Resolve the scripts directory so we can import from it
SCRIPTS_DIR = Path(__file__).parent.parent
sys.path.insert(0, str(SCRIPTS_DIR))

from create_issue import (  # noqa: E402
    GITHUB_BODY_LIMIT,
    render_body,
    run,
    get_label_name,
)

SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_MINIMAL_SLICE = {
    "schema_version": "1.1.0",
    "slice_type": "command",
    "title": "Test slice",
    "summary": "A minimal slice for unit tests.",
    "readiness": "blocked",
    "stakeholder_intent": {
        "actor": "Tester",
        "goal": "Verify correctness",
        "business_value": "Catch regressions early",
        "trigger": "CI run",
        "success_outcome": "Tests pass",
    },
    "scope": {
        "in_scope": ["Unit testing"],
        "out_of_scope": ["Production use"],
    },
    "preconditions": ["Test environment configured"],
    "business_rules": [
        {
            "id": "BR-001",
            "rule": "Tests must pass",
            "applies_when": "Always",
            "outcome": "Exit 0",
        }
    ],
    "decision_tables": [],
    "interfaces": [
        {
            "id": "IF-001",
            "kind": "cli",
            "name": "test cli",
            "operation": "python -m unittest",
            "inputs": [],
            "outputs": [{"condition": "success", "status": "ok", "body": "pass"}],
            "errors": [],
        }
    ],
    "read_models": [],
    "process_steps": [],
    "integration_contracts": [],
    "state_changes": [
        {
            "id": "SC-001",
            "target": "test state",
            "precondition": "none",
            "changes": [{"field": "status", "operation": "set", "value_source": "passed"}],
        }
    ],
    "side_effects": [],
    "security": {
        "authentication": "none",
        "authorization_rules": ["none required"],
        "data_classification": "test data",
        "audit_requirements": [],
    },
    "implementation_context": {
        "target_modules": ["tests/"],
        "integration_points": ["none"],
        "architecture_constraints": ["stdlib only"],
        "allowed_changes": ["tests/"],
        "forbidden_changes": [],
        "dependencies": [],
        "migration_strategy": "none",
        "consistency_and_concurrency": "single-threaded test runner",
    },
    "acceptance_scenarios": [
        {
            "id": "AC-001",
            "title": "Happy path",
            "given": ["system ready"],
            "when": ["test runs"],
            "then": ["tests pass"],
            "traces_to": ["BR-001", "IF-001", "SC-001"],
        }
    ],
    "quality": {
        "required_test_levels": ["unit"],
        "test_cases": ["happy path"],
        "quality_commands": ["python3 -m unittest discover"],
        "non_functional_requirements": [],
        "definition_of_done": ["all tests green"],
    },
    "decision_log": [
        {
            "id": "D-001",
            "topic": "test framework",
            "decision": "use stdlib unittest",
            "source": "user",
            "evidence_ids": [],
            "rationale": "no external deps",
        }
    ],
    "codebase_evidence": [],
    "open_questions": ["nothing — just a test fixture"],
    "assumptions_requiring_confirmation": [],
    "blockers": [],
}


def _write_slice(data: dict, path: Path) -> None:
    path.write_text(json.dumps(data, indent=2), encoding="utf-8")


def _noop_validate(path: Path):
    return []


def _fail_validate(path: Path):
    return ["$.title: required field missing", "semantic error X"]


FAKE_ISSUE_URL = "https://github.com/owner/repo/issues/42"


class FakeResult:
    def __init__(self, returncode: int, stdout: str = "", stderr: str = ""):
        self.returncode = returncode
        self.stdout = stdout
        self.stderr = stderr


class FakeGhRunner:
    """Records all calls; configurable failure modes."""

    def __init__(
        self,
        auth_ok: bool = True,
        create_url: str = FAKE_ISSUE_URL,
        blocked_by_fail: bool = False,
        label_create_fail: bool = False,
        label_already_exists: bool = False,
        label_attach_fail: bool = False,
    ):
        self.calls: list = []
        self._auth_ok = auth_ok
        self._create_url = create_url
        self._blocked_by_fail = blocked_by_fail
        self._label_create_fail = label_create_fail
        self._label_already_exists = label_already_exists
        self._label_attach_fail = label_attach_fail

    def run(self, args, *, check=True, capture_output=False):
        self.calls.append(list(args))
        verb = tuple(args[:2])

        if verb == ("auth", "status"):
            if not self._auth_ok:
                if check:
                    raise subprocess.CalledProcessError(1, "gh", stderr="not authenticated")
                return FakeResult(returncode=1, stderr="not authenticated")
            return FakeResult(returncode=0)

        if verb == ("issue", "create"):
            return FakeResult(returncode=0, stdout=self._create_url)

        if args[0] == "api" and "-q" in args and ".id" in args:
            # dependency id resolution (GET /repos/.../issues/<n> -q .id)
            return FakeResult(returncode=0, stdout="424242")

        if args[0] == "api" and "dependencies/blocked_by" in " ".join(args):
            if self._blocked_by_fail:
                if check:
                    raise subprocess.CalledProcessError(1, "gh", stderr="blocked-by failed")
                return FakeResult(returncode=1, stderr="blocked-by failed")
            return FakeResult(returncode=0, stdout="{}")

        if verb == ("label", "create"):
            # _ensure_label always uses check=False, so check is always False here.
            # Return appropriate result; _ensure_label inspects returncode + stderr.
            if self._label_already_exists:
                return FakeResult(returncode=1, stderr="label 'x' already exists in the repository")
            if self._label_create_fail:
                return FakeResult(returncode=1, stderr="internal server error")
            return FakeResult(returncode=0)

        if verb == ("issue", "edit"):
            if self._label_attach_fail:
                if check:
                    raise subprocess.CalledProcessError(1, "gh", stderr="label attach failed")
                return FakeResult(returncode=1, stderr="label attach failed")
            return FakeResult(returncode=0)

        return FakeResult(returncode=0)

    def call_verbs(self) -> list:
        return [tuple(c[:2]) for c in self.calls]

    def had_write(self) -> bool:
        write_verbs = {("issue", "create"), ("issue", "edit"), ("label", "create")}
        api_calls = [c for c in self.calls if c[0] == "api"]
        return bool(
            write_verbs.intersection(self.call_verbs()) or api_calls
        )


def _fake_git_ok(args, *, check=False, capture_output=False, text=False, cwd=None):
    return FakeResult(returncode=0, stdout="https://github.com/owner/repo.git")


# ---------------------------------------------------------------------------
# AC-002: Deterministic rendering
# ---------------------------------------------------------------------------

class TestDeterminism(unittest.TestCase):
    def test_render_body_is_byte_identical_on_repeated_calls(self):
        canonical = json.dumps(_MINIMAL_SLICE, indent=2, ensure_ascii=False)
        body1 = render_body(_MINIMAL_SLICE, canonical)
        body2 = render_body(_MINIMAL_SLICE, canonical)
        self.assertEqual(body1, body2)

    def test_embedded_json_round_trips_to_exact_input(self):
        canonical = json.dumps(_MINIMAL_SLICE, indent=2, ensure_ascii=False)
        body = render_body(_MINIMAL_SLICE, canonical)
        # Extract content between ```json ... ```
        start = body.index("```json\n") + len("```json\n")
        end = body.index("\n```", start)
        extracted = body[start:end]
        self.assertEqual(json.loads(extracted), _MINIMAL_SLICE)

    def test_body_contains_authoritative_spec_statement(self):
        canonical = json.dumps(_MINIMAL_SLICE, indent=2)
        body = render_body(_MINIMAL_SLICE, canonical)
        self.assertIn("authoritative machine-readable specification", body)

    def test_body_contains_details_marker(self):
        from create_issue import DETAILS_MARKER
        canonical = json.dumps(_MINIMAL_SLICE, indent=2)
        body = render_body(_MINIMAL_SLICE, canonical)
        self.assertIn(DETAILS_MARKER, body)


# ---------------------------------------------------------------------------
# AC-001: Write sequence ordering (create → blocked_by → label)
# ---------------------------------------------------------------------------

class TestWriteSequence(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.slice_path = Path(self.tmp.name) / "slice.json"
        _write_slice(_MINIMAL_SLICE, self.slice_path)
        self.cwd = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    @patch("subprocess.run")
    def test_create_then_blocked_by_then_label(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner()
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[7, 8],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertEqual(rc, 0)
        verbs = gh.call_verbs()
        # auth status comes first
        self.assertEqual(verbs[0], ("auth", "status"))
        # issue create
        create_idx = next(i for i, v in enumerate(verbs) if v == ("issue", "create"))
        # api calls: one id-resolution GET + one POST per dependency
        api_calls = [c for c in gh.calls if c[0] == "api"]
        self.assertEqual(len(api_calls), 4)
        api_idx = next(i for i, v in enumerate(verbs) if v == ("api", "--method"))
        # label create + attach come after api calls
        label_create_idx = next(
            i for i, v in enumerate(verbs) if v == ("label", "create")
        )
        label_attach_idx = next(
            i for i, v in enumerate(verbs) if v == ("issue", "edit")
        )
        self.assertLess(create_idx, api_idx)
        self.assertLess(api_idx, label_create_idx)
        self.assertLess(label_create_idx, label_attach_idx)

    @patch("subprocess.run")
    def test_label_is_last_write(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner()
        run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        verbs = gh.call_verbs()
        last_write = [v for v in verbs if v in {("issue", "create"), ("issue", "edit"), ("label", "create")}]
        self.assertEqual(last_write[-1], ("issue", "edit"))


# ---------------------------------------------------------------------------
# AC-003: Validation failure creates nothing
# ---------------------------------------------------------------------------

class TestValidationFailure(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.slice_path = Path(self.tmp.name) / "slice.json"
        _write_slice(_MINIMAL_SLICE, self.slice_path)
        self.cwd = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_validation_failure_returns_nonzero(self):
        gh = FakeGhRunner()
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_fail_validate,
        )
        self.assertNotEqual(rc, 0)

    def test_validation_failure_no_writes(self):
        gh = FakeGhRunner()
        run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_fail_validate,
        )
        self.assertFalse(gh.had_write())

    def test_validation_runs_even_in_dry_run(self):
        gh = FakeGhRunner()
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=True,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_fail_validate,
        )
        self.assertNotEqual(rc, 0)
        self.assertFalse(gh.had_write())


# ---------------------------------------------------------------------------
# AC-004: Body size limit fail-closed
# ---------------------------------------------------------------------------

def _body_len_for_summary(summary: str) -> int:
    data = dict(_MINIMAL_SLICE)
    data["summary"] = summary
    canonical = json.dumps(data, indent=2, ensure_ascii=False)
    return len(render_body(data, canonical))


def _find_boundary_sizes():
    """Binary-search for smallest summary size where render_body exceeds GITHUB_BODY_LIMIT."""
    lo, hi = 0, GITHUB_BODY_LIMIT * 2
    while lo < hi:
        mid = (lo + hi) // 2
        if _body_len_for_summary("x" * mid) <= GITHUB_BODY_LIMIT:
            lo = mid + 1
        else:
            hi = mid
    # lo = first size where body > limit; lo-1 = last size where body <= limit
    return lo - 1, lo  # (under_size, over_size)


class TestBodySizeLimit(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        under_size, over_size = _find_boundary_sizes()
        cls._under_size = under_size
        cls._over_size = over_size
        cls._under_body_len = _body_len_for_summary("x" * under_size)
        cls._over_body_len = _body_len_for_summary("x" * over_size)

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.cwd = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def _make_slice(self, summary_size: int) -> dict:
        data = dict(_MINIMAL_SLICE)
        data["summary"] = "x" * summary_size
        return data

    def _run_slice(self, data: dict) -> tuple:
        path = Path(self.tmp.name) / "slice.json"
        _write_slice(data, path)
        gh = FakeGhRunner()
        rc = run(
            slice_path=path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        return rc, gh

    def test_boundary_sizes_are_adjacent(self):
        """Sanity: over_size = under_size + 1 and body lengths straddle the limit."""
        self.assertEqual(self._over_size, self._under_size + 1)
        self.assertLessEqual(self._under_body_len, GITHUB_BODY_LIMIT)
        self.assertGreater(self._over_body_len, GITHUB_BODY_LIMIT)

    @patch("subprocess.run")
    def test_body_at_limit_write_proceeds(self, mock_subprocess):
        """Body <= 65536 chars: issue create is called and run exits 0."""
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        self.assertLessEqual(self._under_body_len, GITHUB_BODY_LIMIT)
        rc, gh = self._run_slice(self._make_slice(self._under_size))
        self.assertEqual(rc, 0)
        self.assertIn(("issue", "create"), gh.call_verbs())

    def test_body_over_limit_fail_closed_no_write(self):
        """Body > 65536 chars: exit non-zero before any write, no gh write calls."""
        self.assertGreater(self._over_body_len, GITHUB_BODY_LIMIT)
        rc, gh = self._run_slice(self._make_slice(self._over_size))
        self.assertNotEqual(rc, 0)
        self.assertFalse(gh.had_write())


# ---------------------------------------------------------------------------
# AC-005: Partial failure — blocked_by step fails after creation
# ---------------------------------------------------------------------------

class TestPartialFailureBlockedBy(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.slice_path = Path(self.tmp.name) / "slice.json"
        _write_slice(_MINIMAL_SLICE, self.slice_path)
        self.cwd = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    @patch("subprocess.run")
    def test_blocked_by_failure_exits_nonzero(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(blocked_by_fail=True)
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[5],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertNotEqual(rc, 0)

    @patch("subprocess.run")
    def test_blocked_by_failure_no_label_attached(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(blocked_by_fail=True)
        run(
            slice_path=self.slice_path,
            blocked_by=[5],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertNotIn(("issue", "edit"), gh.call_verbs())

    @patch("subprocess.run")
    def test_blocked_by_failure_no_issue_delete(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(blocked_by_fail=True)
        run(
            slice_path=self.slice_path,
            blocked_by=[5],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        # No delete or close calls
        for call in gh.calls:
            self.assertNotIn("delete", call)
            self.assertNotIn("close", call)


# ---------------------------------------------------------------------------
# AC-006: Missing label created idempotently
# ---------------------------------------------------------------------------

class TestIdempotentLabelCreate(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.slice_path = Path(self.tmp.name) / "slice.json"
        _write_slice(_MINIMAL_SLICE, self.slice_path)
        self.cwd = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    @patch("subprocess.run")
    def test_label_create_called_before_label_attach(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner()
        run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        verbs = gh.call_verbs()
        self.assertIn(("label", "create"), verbs)
        create_idx = next(i for i, v in enumerate(verbs) if v == ("label", "create"))
        attach_idx = next(i for i, v in enumerate(verbs) if v == ("issue", "edit"))
        self.assertLess(create_idx, attach_idx)

    @patch("subprocess.run")
    def test_label_already_exists_is_idempotent_success(self, mock_subprocess):
        """gh label create returning 'already exists' must be treated as success."""
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(label_already_exists=True)
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertEqual(rc, 0)
        self.assertIn(("issue", "edit"), gh.call_verbs())

    @patch("subprocess.run")
    def test_real_label_create_failure_is_partial_failure(self, mock_subprocess):
        """Any gh label create failure other than 'already exists' is PARTIAL_FAILURE."""
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(label_create_fail=True)
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertNotEqual(rc, 0)
        # Issue was already created before label step
        self.assertIn(("issue", "create"), gh.call_verbs())
        # Label attach was never reached
        self.assertNotIn(("issue", "edit"), gh.call_verbs())

    @patch("subprocess.run")
    def test_label_attach_failure_is_partial_failure(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(label_attach_fail=True)
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertNotEqual(rc, 0)


# ---------------------------------------------------------------------------
# AC-007: Dry run executes no writes
# ---------------------------------------------------------------------------

class TestDryRun(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.slice_path = Path(self.tmp.name) / "slice.json"
        _write_slice(_MINIMAL_SLICE, self.slice_path)
        self.cwd = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_dry_run_exits_zero(self):
        gh = FakeGhRunner()
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[1, 2],
            dry_run=True,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertEqual(rc, 0)

    def test_dry_run_no_writes(self):
        gh = FakeGhRunner()
        run(
            slice_path=self.slice_path,
            blocked_by=[1, 2],
            dry_run=True,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertFalse(gh.had_write())

    def test_dry_run_gh_runner_never_called(self):
        gh = FakeGhRunner()
        run(
            slice_path=self.slice_path,
            blocked_by=[1, 2],
            dry_run=True,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertEqual(gh.calls, [])


# ---------------------------------------------------------------------------
# AC-009: Schema cut-over
# ---------------------------------------------------------------------------

class TestSchemaCutOver(unittest.TestCase):
    def test_dead_script_next_backlog_slot_deleted(self):
        self.assertFalse((SCRIPTS_DIR / "next_backlog_slot.py").exists())

    def test_dead_script_issue_graph_deleted(self):
        self.assertFalse((SCRIPTS_DIR / "issue_graph.py").exists())

    def test_skill_md_ends_in_create_issue(self):
        skill_md = SCRIPTS_DIR.parent / "SKILL.md"
        content = skill_md.read_text(encoding="utf-8")
        self.assertIn("create_issue.py", content)
        self.assertNotIn("docs/backlog", content)
        self.assertNotIn("next_backlog_slot", content)
        self.assertNotIn("issue_graph", content)

    def test_schema_does_not_require_slice_id(self):
        schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
        self.assertNotIn("slice_id", schema.get("required", []))

    def test_schema_does_not_have_slice_id_property(self):
        schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
        self.assertNotIn("slice_id", schema.get("properties", {}))

    def test_schema_depends_on_is_informational(self):
        schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
        depends_on = schema.get("properties", {}).get("depends_on", {})
        # Items must NOT be restricted to kebab-case IDs
        items = depends_on.get("items", {})
        self.assertNotEqual(items.get("$ref"), "#/$defs/kebabId")

    def test_scaffold_has_no_slice_id(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location(
            "schema_scaffold", SCRIPTS_DIR / "schema-scaffold.py"
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "out.json"
            mod.scaffold_from_schema(str(SCHEMA_PATH), "command", str(out))
            data = json.loads(out.read_text())
        self.assertNotIn("slice_id", data)

    def test_validate_accepts_slice_without_slice_id(self):
        """A slice without slice_id must pass full schema validation."""
        try:
            from jsonschema import Draft202012Validator
        except ImportError:
            self.skipTest("jsonschema not installed")

        schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
        # Use the minimal test fixture (no slice_id)
        validator = Draft202012Validator(schema)
        errors = list(validator.iter_errors(_MINIMAL_SLICE))
        self.assertEqual(errors, [], [str(e) for e in errors])

    def test_validate_accepts_prose_depends_on(self):
        """depends_on with prose strings must pass schema validation."""
        try:
            from jsonschema import Draft202012Validator
        except ImportError:
            self.skipTest("jsonschema not installed")

        schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
        data = dict(_MINIMAL_SLICE)
        data["depends_on"] = ["Requires issue #5 to be closed first"]
        validator = Draft202012Validator(schema)
        errors = list(validator.iter_errors(data))
        self.assertEqual(errors, [], [str(e) for e in errors])


# ---------------------------------------------------------------------------
# P2-1: CLI-level --blocked-by validation
# ---------------------------------------------------------------------------

class TestBlockedByValidation(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.slice_path = Path(self.tmp.name) / "slice.json"
        _write_slice(_MINIMAL_SLICE, self.slice_path)

    def tearDown(self):
        self.tmp.cleanup()

    def _call_main(self, blocked_by_arg: str) -> int:
        from create_issue import main
        argv = ["create_issue.py", str(self.slice_path), "--blocked-by", blocked_by_arg]
        with patch("sys.argv", argv):
            return main()

    def test_zero_rejected(self):
        rc = self._call_main("0")
        self.assertNotEqual(rc, 0)

    def test_negative_rejected(self):
        rc = self._call_main("-1")
        self.assertNotEqual(rc, 0)

    def test_malformed_non_integer_rejected(self):
        rc = self._call_main("abc")
        self.assertNotEqual(rc, 0)

    def test_mixed_valid_and_zero_rejected(self):
        rc = self._call_main("5,0,3")
        self.assertNotEqual(rc, 0)

    def test_mixed_valid_and_negative_rejected(self):
        rc = self._call_main("1,-2,3")
        self.assertNotEqual(rc, 0)


# ---------------------------------------------------------------------------
# AC-010: Auth precondition failure → no writes
# ---------------------------------------------------------------------------

class TestAuthPrecondition(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.slice_path = Path(self.tmp.name) / "slice.json"
        _write_slice(_MINIMAL_SLICE, self.slice_path)
        self.cwd = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    @patch("subprocess.run")
    def test_auth_failure_exits_nonzero(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(auth_ok=False)
        rc = run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertNotEqual(rc, 0)

    @patch("subprocess.run")
    def test_auth_failure_no_writes(self, mock_subprocess):
        mock_subprocess.return_value = FakeResult(
            returncode=0, stdout="https://github.com/owner/repo.git"
        )
        gh = FakeGhRunner(auth_ok=False)
        run(
            slice_path=self.slice_path,
            blocked_by=[],
            dry_run=False,
            gh=gh,
            cwd=self.cwd,
            validate_fn=_noop_validate,
        )
        self.assertNotIn(("issue", "create"), gh.call_verbs())
        self.assertFalse(gh.had_write())

    def test_label_config_default_when_no_config_file(self):
        self.assertEqual(get_label_name({}), "ready-for-agent")

    def test_label_config_reads_from_config(self):
        self.assertEqual(get_label_name({"label": "my-label"}), "my-label")


if __name__ == "__main__":
    unittest.main()
