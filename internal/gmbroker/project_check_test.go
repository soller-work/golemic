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
	if _, hasStdout := result["stdout"]; hasStdout {
		t.Fatal("result must not include stdout field")
	}
	if _, hasStderr := result["stderr"]; hasStderr {
		t.Fatal("result must not include stderr field")
	}
	outputFile, ok := result["outputFile"].(string)
	if !ok || outputFile == "" {
		t.Fatalf("outputFile: got %v, want non-empty path", result["outputFile"])
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
	if _, hasStdout := result["stdout"]; hasStdout {
		t.Fatal("result must not include stdout field")
	}
	if _, hasStderr := result["stderr"]; hasStderr {
		t.Fatal("result must not include stderr field")
	}
	outputFile, ok := result["outputFile"].(string)
	if !ok || outputFile == "" {
		t.Fatalf("outputFile: got %v, want non-empty path", result["outputFile"])
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

func TestProjectCheck_OutputFile_ContainsFullOutput(t *testing.T) {
	dir := initProjectCheckRepo(t)
	verify := `i=1; while [ $i -le 250 ]; do echo "stdout-$i"; echo "stderr-$i" >&2; i=$((i+1)); done`
	_, sockPath := startProjectCheckBroker(t, dir, verify, []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	if result["ok"] != true {
		t.Fatalf("ok: got %v, want true", result["ok"])
	}

	outputFile, ok := result["outputFile"].(string)
	if !ok || outputFile == "" {
		t.Fatalf("outputFile missing or empty: %v", result["outputFile"])
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read outputFile: %v", err)
	}
	log := string(content)
	fp := result["workingTreeFingerprint"].(string)
	assertLogContains(t, log, fp)
}

func assertLogContains(t *testing.T, log, fingerprint string) {
	t.Helper()
	for _, want := range []string{"=== STDOUT ===", "=== STDERR ===", "stdout-250", "stderr-250", "exitCode: 0", fingerprint} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q; got:\n%s", want, log[:min(500, len(log))])
		}
	}
}

func TestProjectCheck_OutputFile_OutsideWorktree(t *testing.T) {
	dir := initProjectCheckRepo(t)
	_, sockPath := startProjectCheckBroker(t, dir, "echo ok", []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	outputFile, ok := result["outputFile"].(string)
	if !ok || outputFile == "" {
		t.Fatalf("outputFile missing: %v", result["outputFile"])
	}
	if strings.HasPrefix(outputFile, dir) {
		t.Fatalf("outputFile %q must not be inside worktree %q", outputFile, dir)
	}
}

func TestProjectCheck_OutputFile_CleanedUpOnShutdown(t *testing.T) {
	dir := initProjectCheckRepo(t)
	b, sockPath := startProjectCheckBroker(t, dir, "echo ok", []string{"gm_slice_get", "gm_project_check", "gm_dev_done", "gm_review_submit"})

	result := call(t, sockPath, "gm_project_check", "c1", map[string]any{})
	outputFile, ok := result["outputFile"].(string)
	if !ok || outputFile == "" {
		t.Fatalf("outputFile missing: %v", result["outputFile"])
	}

	if _, err := os.Stat(outputFile); err != nil {
		t.Fatalf("outputFile should exist before shutdown: %v", err)
	}

	b.Shutdown()
	// prevent double-Shutdown from t.Cleanup
	t.Cleanup(func() {})

	if _, err := os.Stat(outputFile); !os.IsNotExist(err) {
		t.Fatalf("outputFile should be removed after shutdown, got: %v", err)
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

var _ = json.RawMessage{}
