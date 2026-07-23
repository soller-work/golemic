package gmbroker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/worktreefingerprint"
)

type gitOnlyExecutor struct{}

func (gitOnlyExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initProjectCheckRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "user.name", "Test User")
	writeTestFile(t, filepath.Join(dir, "tracked.txt"), "tracked\n")
	writeTestFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	gitCmd(t, dir, "add", "tracked.txt", ".gitignore")
	gitCmd(t, dir, "commit", "-m", "init")
	writeTestFile(t, filepath.Join(dir, "ignored.txt"), "ignored\n")
	return dir
}

func startProjectCheckBroker(t *testing.T, worktree, verifyCommand string, allowed []string) (*Broker, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gmb*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	sockPath := filepath.Join(dir, "gm.sock")
	b, err := StartWithFetcherAndProjectCheck(sockPath, func(_ context.Context) (string, error) { return "spec", nil }, ProjectCheckConfig{
		WorktreePath:  worktree,
		VerifyCommand: verifyCommand,
	}, allowed)
	if err != nil {
		t.Fatalf("StartWithFetcherAndProjectCheck: %v", err)
	}
	t.Cleanup(b.Shutdown)
	return b, sockPath
}

func fingerprintOf(t *testing.T, dir string) string {
	t.Helper()
	fp, err := worktreefingerprint.Compute(dir, gitOnlyExecutor{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return fp
}

func TestProjectCheck_PassesWithFingerprint(t *testing.T) {
	dir := initProjectCheckRepo(t)
	_, sockPath := startProjectCheckBroker(t, dir, "echo pass", []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	if result["ok"] != true {
		t.Fatalf("ok: got %v, want true", result["ok"])
	}
	if result["exitCode"] != float64(0) {
		t.Fatalf("exitCode: got %v, want 0", result["exitCode"])
	}
	if result["summary"] != "verify passed" {
		t.Fatalf("summary: got %v", result["summary"])
	}
	if got := result["workingTreeFingerprint"]; got != fingerprintOf(t, dir) {
		t.Fatalf("fingerprint: got %v, want current tree fingerprint", got)
	}
}

func TestProjectCheck_FailureReturnsExitCode(t *testing.T) {
	dir := initProjectCheckRepo(t)
	_, sockPath := startProjectCheckBroker(t, dir, "printf fail-out; printf fail-err >&2; exit 7", []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	if result["ok"] != false {
		t.Fatalf("ok: got %v, want false", result["ok"])
	}
	if result["exitCode"] != float64(7) {
		t.Fatalf("exitCode: got %v, want 7", result["exitCode"])
	}
	if result["summary"] != "verify failed (exit 7)" {
		t.Fatalf("summary: got %v", result["summary"])
	}
	if !strings.HasPrefix(result["workingTreeFingerprint"].(string), "sha256:") {
		t.Fatalf("fingerprint: got %v", result["workingTreeFingerprint"])
	}
}

func TestProjectCheck_FingerprintCapturedAfterMutation(t *testing.T) {
	dir := initProjectCheckRepo(t)
	before := fingerprintOf(t, dir)
	_, sockPath := startProjectCheckBroker(t, dir, "printf generated > generated.txt", []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	after := fingerprintOf(t, dir)

	if result["ok"] != true {
		t.Fatalf("ok: got %v, want true", result["ok"])
	}
	if result["workingTreeFingerprint"] != after {
		t.Fatalf("fingerprint: got %v, want %v", result["workingTreeFingerprint"], after)
	}
	if before == after {
		t.Fatal("mutation did not change fingerprint")
	}
}

func TestProjectCheck_OutputModes(t *testing.T) {
	dir := initProjectCheckRepo(t)
	verify := `i=1; while [ $i -le 250 ]; do echo "stdout-$i"; echo "stderr-$i" >&2; i=$((i+1)); done`
	_, sockPath := startProjectCheckBroker(t, dir, verify, []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	defaultResult := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	fullResult := call(t, sockPath, "gm_project_check", "c2", map[string]any{"output": "full"})

	if defaultResult["ok"] != fullResult["ok"] || defaultResult["exitCode"] != fullResult["exitCode"] {
		t.Fatalf("verdict changed across output modes: default=%v full=%v", defaultResult, fullResult)
	}
	if defaultResult["workingTreeFingerprint"] != fullResult["workingTreeFingerprint"] {
		t.Fatalf("fingerprint changed across output modes: default=%v full=%v", defaultResult["workingTreeFingerprint"], fullResult["workingTreeFingerprint"])
	}
	if !strings.Contains(defaultResult["stdout"].(string), "truncated") && !strings.Contains(defaultResult["stderr"].(string), "truncated") {
		t.Fatalf("default output was not truncated: stdout=%q stderr=%q", defaultResult["stdout"], defaultResult["stderr"])
	}
	if !strings.Contains(fullResult["stdout"].(string), "stdout-250") || !strings.Contains(fullResult["stderr"].(string), "stderr-250") {
		t.Fatalf("full output missing tail lines: %q / %q", fullResult["stdout"], fullResult["stderr"])
	}
}

func TestProjectCheck_OutputModes_ByteCap(t *testing.T) {
	dir := initProjectCheckRepo(t)
	// Single line of ~64KB to exceed the 32KB byte cap without hitting the line cap.
	verify := `python3 -c "import sys; sys.stdout.write('X' * 65536 + '\n')"` + ` || ` +
		`node -e "process.stdout.write('X'.repeat(65536) + '\n')"` + ` || ` +
		`perl -e "print 'X' x 65536; print \"\\n\""`
	_, sockPath := startProjectCheckBroker(t, dir, verify, []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	cappedResult := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	fullResult := call(t, sockPath, "gm_project_check", "c2", map[string]any{"output": "full"})

	if cappedResult["ok"] != fullResult["ok"] || cappedResult["exitCode"] != fullResult["exitCode"] {
		t.Fatalf("verdict changed across output modes: capped=%v full=%v", cappedResult, fullResult)
	}
	if cappedResult["workingTreeFingerprint"] != fullResult["workingTreeFingerprint"] {
		t.Fatalf("fingerprint changed: capped=%v full=%v", cappedResult["workingTreeFingerprint"], fullResult["workingTreeFingerprint"])
	}

	cappedStdout := cappedResult["stdout"].(string)
	const maxBytes = 32 * 1024
	if len(cappedStdout) > maxBytes+200 {
		t.Fatalf("capped stdout too long: %d bytes", len(cappedStdout))
	}
	if !strings.Contains(cappedStdout, "bytes truncated") {
		t.Fatalf("byte-truncation marker missing in capped stdout: %q", cappedStdout[:min(200, len(cappedStdout))])
	}

	fullStdout := fullResult["stdout"].(string)
	if len(fullStdout) < 65536 {
		t.Fatalf("full stdout was truncated: %d bytes", len(fullStdout))
	}
}

func TestProjectCheck_ReviewerAllowlistExcludesTool(t *testing.T) {
	dir := initProjectCheckRepo(t)
	_, sockPath := startProjectCheckBroker(t, dir, "echo pass", []string{"gm_slice_get", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	if result["ok"] != false {
		t.Fatalf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "UNKNOWN_TOOL" {
		t.Fatalf("code: got %v, want UNKNOWN_TOOL", result["code"])
	}
}

func TestProjectCheck_ValidatesSchema(t *testing.T) {
	dir := initProjectCheckRepo(t)
	_, sockPath := startProjectCheckBroker(t, dir, "echo pass", []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{"output": "bogus"})
	if result["ok"] != false {
		t.Fatalf("ok: got %v, want false", result["ok"])
	}
	if result["code"] != "SCHEMA_INVALID" {
		t.Fatalf("code: got %v, want SCHEMA_INVALID", result["code"])
	}
}

var _ = json.RawMessage{}
