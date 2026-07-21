"""Tests for create_issue.py v2."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest


SCRIPTS_DIR = Path(__file__).parent.parent
CREATE_ISSUE_PY = SCRIPTS_DIR / "create_issue.py"
SCHEMA_PATH = SCRIPTS_DIR.parent / "schema.json"


def load_minimal_slice():
    """Create a minimal valid slice dict."""
    return {
        "slice_type": "command",
        "change_type": "feature",
        "title": "Test Issue",
        "stakeholder": "User",
        "trigger": "User action",
        "success_outcome": "State updated",
        "tldr": "Update state",
        "scope": {"in": ["Feature A"], "out": ["Feature B"]},
        "behavior": "Mutate state.",
        "business_rules": "",
        "acceptance_scenarios": ["Given X, when Y, then Z"],
        "inputs_outputs_errors": "Input: event. Output: state.",
        "proof": {
            "how": "We run the action and see the state change.",
            "why": "The observed change is exactly the promised outcome.",
            "checks": [{"functional": "Action updates state", "technical": "A test asserts the state mutation"}],
        },
        "verify_commands": ["pytest"],
        "definition_of_done": ["Tests pass"],
        "readiness": "ready",
        "blockers": [],
        "security_relevant": False,
    }


def run_create_issue_py(*args):
    """Run create_issue.py and return (returncode, stdout, stderr)."""
    result = subprocess.run(
        [sys.executable, str(CREATE_ISSUE_PY)] + list(args),
        capture_output=True,
        text=True,
    )
    return result.returncode, result.stdout, result.stderr


class TestCreateIssueRender:
    """Tests for fixed body layout."""

    def test_dry_run_renders_tldr_header(self):
        """Test --dry-run renders TL;DR header with change_type, slice_type, and tldr."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0, f"dry-run should succeed: {stderr}"
            assert "TL;DR" in stdout
            assert "feature" in stdout
            assert "command" in stdout
            assert "Update state" in stdout

    def test_fixed_section_order_no_security(self):
        """Test all sections present in fixed order when security_relevant=false."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["security_relevant"] = False
            data["blockers"] = []
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0

            # Extract section headers
            lines = stdout.split("\n")
            sections = [l for l in lines if l.startswith("## ")]
            section_names = [s.replace("## ", "").strip() for s in sections]

            # Expected sections when security_relevant=false and blockers empty
            expected = [
                "Stakeholder & Intent",
                "Scope",
                "Behavior",
                "Business Rules",
                "Acceptance Scenarios",
                "Proof of Delivery",
                "Inputs / Outputs / Errors",
                "Codebase Evidence",
                "Verify",
                "Definition of Done",
            ]

            assert section_names == expected, f"Got sections: {section_names}"
            # Security and Blockers should NOT be present
            assert "Security" not in section_names
            assert "Blockers / Open Questions" not in section_names

    def test_proof_section_renders_how_why_and_checklist(self):
        """Proof of Delivery renders how, why, and a functional/technical checklist table."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["proof"] = {
                "how": "We run the action and watch the result.",
                "why": "The result is exactly the promise.",
                "checks": [
                    {"functional": "Order is cancelled", "technical": "A test asserts status becomes cancelled"},
                ],
            }
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0, stderr
            assert "## Proof of Delivery" in stdout
            assert "We run the action and watch the result." in stdout
            assert "The result is exactly the promise." in stdout
            assert "| Functional (stakeholder) | Technical evidence (reviewer confirms) |" in stdout
            assert "| Order is cancelled | A test asserts status becomes cancelled |" in stdout

    def test_proof_checklist_normalizes_newlines_in_cells(self):
        """Test that multiline functional/technical text is normalized to spaces, keeping cells in single row."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["proof"] = {
                "how": "Run test.",
                "why": "Confirms behavior.",
                "checks": [
                    {
                        "functional": "User action\nresults in\nstate change",
                        "technical": "Test with\r\nmultiple\nlines should work",
                    },
                    {
                        "functional": "Single-line\rCR normalized",
                        "technical": "Pipe | char must be escaped",
                    },
                ],
            }
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0, stderr

            # Find all table rows (lines starting with |)
            lines = stdout.split("\n")
            table_rows = [line for line in lines if line.startswith("|")]

            # Should have header, separator, and two data rows
            assert len(table_rows) >= 4, f"Expected at least 4 table rows (header + sep + 2 data), got {len(table_rows)}: {table_rows}"

            # Verify multiline content is normalized (all on one row, spaces replace newlines)
            assert "User action results in state change" in table_rows[2], "Newlines should be converted to spaces"
            assert "Test with multiple lines should work" in table_rows[2], "CR/LF should be converted to spaces"
            assert "Single-line CR normalized" in table_rows[3], "CR should be converted to space"
            assert "Pipe \\| char must be escaped" in table_rows[3], "Pipes should be escaped"
            # Verify no actual newlines within the data rows
            for row in table_rows[2:]:
                assert "\n" not in row, f"Row contains actual newline (broken table cell): {repr(row)}"

    def test_fixed_section_order_with_security_and_blockers(self):
        """Test all sections present when security_relevant=true and blockers non-empty."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "blocked"
            data["security_relevant"] = True
            data["security"] = "Requires auth."
            data["blockers"] = [{"kind": "question", "text": "Is this needed?"}]
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0

            lines = stdout.split("\n")
            sections = [l for l in lines if l.startswith("## ")]
            section_names = [s.replace("## ", "").strip() for s in sections]

            expected = [
                "Stakeholder & Intent",
                "Scope",
                "Behavior",
                "Business Rules",
                "Acceptance Scenarios",
                "Proof of Delivery",
                "Inputs / Outputs / Errors",
                "Codebase Evidence",
                "Security",
                "Verify",
                "Definition of Done",
                "Blockers / Open Questions",
            ]

            assert section_names == expected, f"Got sections: {section_names}"
            assert "Security" in section_names
            assert "Blockers / Open Questions" in section_names

    def test_empty_sections_render_none_placeholder(self):
        """Test empty sections render _None_ placeholder."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["business_rules"] = ""  # empty
            data["verify_commands"] = []  # empty
            data["definition_of_done"] = []  # empty
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0

            # Check for _None_ placeholders in empty sections
            assert "_None_" in stdout

    def test_conditional_security_section_present(self):
        """Test Security section rendered when security_relevant=true."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "blocked"
            data["security_relevant"] = True
            data["security"] = "Requires auth."
            data["blockers"] = [{"kind": "blocker", "text": "Auth not implemented"}]
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0
            assert "## Security" in stdout
            assert "Requires auth" in stdout

    def test_conditional_security_section_absent(self):
        """Test Security section NOT rendered when security_relevant=false."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["security_relevant"] = False
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0
            lines = stdout.split("\n")
            security_sections = [l for l in lines if l.strip() == "## Security"]
            assert len(security_sections) == 0

    def test_dry_run_invalid_slice_rejected(self):
        """Test --dry-run fails on invalid slice (semantic validation)."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["readiness"] = "ready"
            data["blockers"] = [{"kind": "question", "text": "Unresolved"}]  # ready with blockers = invalid
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code != 0, "Should reject ready slice with non-empty blockers"
            assert "Validation failed" in stderr

    def test_missing_file(self):
        """Test --dry-run fails on missing file."""
        code, stdout, stderr = run_create_issue_py("/nonexistent/slice.json", "--dry-run")
        assert code != 0
        assert "File not found" in stderr


class TestCreateIssueArchiving:
    """Test JSON archiving (P1-1: move, not copy)."""

    def test_archiving_uses_move(self):
        """Verify that archive_slice uses shutil.move."""
        create_issue_code = CREATE_ISSUE_PY.read_text(encoding="utf-8")
        assert "shutil.move" in create_issue_code
        assert "def archive_slice" in create_issue_code


class TestChangeTypeToIssueType:
    """Tests for change_type → GitHub issue type mapping."""

    @pytest.mark.parametrize("change_type,expected_type", [
        ("feature", "Feature"),
        ("bug", "Bug"),
        ("refactoring", "Task"),
    ])
    def test_dry_run_shows_mapped_issue_type(self, change_type, expected_type):
        with tempfile.TemporaryDirectory() as tmpdir:
            tmpdir = Path(tmpdir)
            slice_path = tmpdir / "slice.json"

            data = load_minimal_slice()
            data["change_type"] = change_type
            if change_type != "feature":
                # Detail fields are gattung-specific; drop the feature ones and
                # supply the correct block for the change_type.
                for key in ("behavior", "business_rules", "acceptance_scenarios", "inputs_outputs_errors"):
                    data.pop(key, None)
            if change_type == "bug":
                data["reproduction"] = "Do X. Observed: crash. Expected: ok."
                data["root_cause"] = "Missing check."
                data["regression_scenarios"] = ["Given X, When Y, Then no crash"]
            elif change_type == "refactoring":
                data["current_structure"] = "God object."
                data["target_structure"] = "Split modules."
                data["behavior_preservation"] = "Identical output for identical input."
            slice_path.write_text(json.dumps(data))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0, f"dry-run failed: {stderr}"
            assert f"--type {expected_type!r}" in stdout




# German function words used as a proxy for non-English prose in artifact fields.
_GERMAN_PROSE_MARKERS = [
    " der ", " die ", " das ", " ein ", " eine ", " ist ", " sind ",
    " und ", " oder ", " aber ", " nicht ", " mit ", " für ", " von ",
    " wird ", " werden ", " kann ", " soll ", " muss ", " beim ",
]


def _contains_german_prose(text: str) -> bool:
    """Return True if text contains German function-word patterns."""
    lower = text.lower()
    return any(marker in lower for marker in _GERMAN_PROSE_MARKERS)


SKILL_MD_PATH = Path(__file__).parent.parent.parent / "SKILL.md"


class TestLanguagePolicy:
    """Regression coverage: German interview → English issue artifacts (issue #149)."""

    def test_skill_md_defines_artifact_language_as_english(self):
        """SKILL.md must explicitly state that generated artifact fields are authored in English."""
        text = SKILL_MD_PATH.read_text(encoding="utf-8")
        assert "Language Policy" in text, "SKILL.md must contain a Language Policy section"
        assert "English" in text, "SKILL.md must reference English as the artifact language"
        # The policy section must cover both interview and artifact sides.
        assert "artifact" in text.lower() or "issue" in text.lower(), (
            "SKILL.md language policy must address artifact / issue language"
        )

    def test_skill_md_allows_non_english_interview(self):
        """SKILL.md must retain German question framing (Frage N) to allow non-English interviews."""
        text = SKILL_MD_PATH.read_text(encoding="utf-8")
        assert "Frage" in text, (
            "SKILL.md must keep the German question format example (Frage N) "
            "to signal that the interview follows the user's session language"
        )

    def test_english_slice_renders_without_german_prose(self):
        """A correctly authored English slice (from any-language interview) renders no German prose."""
        english_slice = {
            "slice_type": "command",
            "change_type": "bug",
            "title": "Fix missing null check in authentication middleware",
            "stakeholder": "Backend developers who maintain the authentication layer.",
            "trigger": "A request arrives without an Authorization header.",
            "success_outcome": "The middleware returns 401 with a structured error body.",
            "tldr": "Add a null-guard for the Authorization header in the auth middleware.",
            "scope": {
                "in": ["Auth middleware null check", "Structured 401 error response"],
                "out": ["Token validation logic", "Session management"],
            },
            "reproduction": (
                "Send a POST /api/login without the Authorization header. "
                "Observed: 500 Internal Server Error. Expected: 401 Unauthorized."
            ),
            "root_cause": "The middleware accesses the header value without checking for its absence.",
            "regression_scenarios": [
                "Given a request without Authorization header, when the middleware processes it, "
                "then the response status is 401 and the body contains an error field."
            ],
            "proof": {
                "how": (
                    "We send a request without the Authorization header and assert the response "
                    "status is 401 with a structured JSON error body."
                ),
                "why": (
                    "This reproduces the exact failure mode and confirms the guard is in place."
                ),
                "checks": [
                    {
                        "functional": "Missing Authorization header returns 401",
                        "technical": "A test sends a request without the header and asserts status 401 and error body",
                    }
                ],
            },
            "verify_commands": ["go test ./internal/middleware/..."],
            "definition_of_done": [
                "Null check added to auth middleware",
                "Regression test passes",
                "go vet and golangci-lint clean",
            ],
            "readiness": "ready",
            "blockers": [],
            "security_relevant": False,
        }

        with tempfile.TemporaryDirectory() as tmpdir:
            slice_path = Path(tmpdir) / "slice.json"
            slice_path.write_text(json.dumps(english_slice))

            code, stdout, stderr = run_create_issue_py(str(slice_path), "--dry-run")
            assert code == 0, f"dry-run failed: {stderr}"

            # Verify that no agent-authored prose field contains German function words.
            prose_fields = [
                english_slice["title"],
                english_slice["stakeholder"],
                english_slice["trigger"],
                english_slice["success_outcome"],
                english_slice["tldr"],
                english_slice["reproduction"],
                english_slice["root_cause"],
                english_slice["proof"]["how"],
                english_slice["proof"]["why"],
            ] + english_slice["regression_scenarios"] + english_slice["definition_of_done"]

            for field_text in prose_fields:
                assert not _contains_german_prose(field_text), (
                    f"English artifact field contains German prose: {field_text!r}"
                )

    def test_denglish_slice_detected_by_language_check(self):
        """A Denglish slice (German prose in artifact fields) is caught by the language check helper."""
        denglish_fields = [
            "Authentifizierungs-Middleware: Null-Prüfung fehlt beim Authorization-Header",
            "Backend-Entwickler, die die Authentifizierungsschicht pflegen.",
            "Eine Anfrage kommt ohne Authorization-Header an.",
            "Die Middleware gibt einen 401-Fehler zurück.",
        ]
        detected = [f for f in denglish_fields if _contains_german_prose(f)]
        assert len(detected) > 0, (
            "Language check helper must detect German prose in Denglish artifact fields"
        )

    def test_technical_identifiers_survive_english_policy(self):
        """Technical identifiers (paths, commands, symbols) are unchanged under the English policy."""
        technical_tokens = [
            "internal/middleware/auth.go",
            "go test ./...",
            "Authorization",
            "#149",
            "`ctx context.Context`",
        ]
        for token in technical_tokens:
            assert not _contains_german_prose(token), (
                f"Technical identifier wrongly flagged as German prose: {token!r}"
            )
