package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeGH creates a fake gh binary in a temp dir that exits 0 and prints "fake-gh".
func writeFakeGH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake-gh\nexit 0\n"), 0755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	return path
}

// TestGHShim_DirectGHBlocked verifies that the shim fails closed when
// GOLEMIC_GH_AUTHORIZED is absent — simulating an agent bash invocation.
func TestGHShim_DirectGHBlocked(t *testing.T) {
	fakeGHPath := writeFakeGH(t)

	shimDir, cleanup, err := createGHShimWithPath(fakeGHPath)
	if err != nil {
		t.Fatalf("createGHShimWithPath: %v", err)
	}
	defer cleanup()

	cmd := exec.Command("sh", "-c", "gh --version")
	cmd.Env = append(filterEnv(os.Environ(), "PATH", "GOLEMIC_GH_AUTHORIZED"),
		"PATH="+shimDir+string(filepath.ListSeparator)+filepath.Dir(fakeGHPath),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err == nil {
		t.Fatal("expected gh to fail in agent env (no GOLEMIC_GH_AUTHORIZED), got success")
	}
	if !strings.Contains(stderr.String(), "golemic guard") {
		t.Errorf("shim should emit golemic guard message, got stderr: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "golemic") {
		t.Errorf("shim error should mention golemic wrapper commands, got: %q", stderr.String())
	}
}

// TestGHShim_AuthorizedGHPasses verifies that the shim delegates to the real gh
// when GOLEMIC_GH_AUTHORIZED=1 is set — simulating golemic's internal gh calls.
func TestGHShim_AuthorizedGHPasses(t *testing.T) {
	fakeGHPath := writeFakeGH(t)

	shimDir, cleanup, err := createGHShimWithPath(fakeGHPath)
	if err != nil {
		t.Fatalf("createGHShimWithPath: %v", err)
	}
	defer cleanup()

	cmd := exec.Command("sh", "-c", "gh --version")
	cmd.Env = append(filterEnv(os.Environ(), "PATH", "GOLEMIC_GH_AUTHORIZED"),
		"PATH="+shimDir+string(filepath.ListSeparator)+filepath.Dir(fakeGHPath),
		"GOLEMIC_GH_AUTHORIZED=1",
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("gh should pass through when GOLEMIC_GH_AUTHORIZED=1, got error: %v", err)
	}
	if !strings.Contains(stdout.String(), "fake-gh") {
		t.Errorf("shim should have passed through to real gh, got stdout: %q", stdout.String())
	}
}

// TestGHGuard_AgentEnvBlocks_Dev verifies that a dev agent subprocess environment
// (as produced by RunRole) blocks direct gh execution.
func TestGHGuard_AgentEnvBlocks_Dev(t *testing.T) {
	testGHGuardAgentEnvBlocks(t, "dev", []string{"read", "bash", "write", "edit"})
}

// TestGHGuard_AgentEnvBlocks_Reviewer verifies that a reviewer agent subprocess
// environment also blocks direct gh execution.
func TestGHGuard_AgentEnvBlocks_Reviewer(t *testing.T) {
	testGHGuardAgentEnvBlocks(t, "reviewer", []string{"read", "bash"})
}

// testGHGuardAgentEnvBlocks is the shared body for dev/reviewer gh guard tests.
// It overrides ghShimCreator so the shim points to a controlled fake gh, then
// runs RunRole with a script that tries to call gh directly and asserts the
// failure message reaches the transcript.
func testGHGuardAgentEnvBlocks(t *testing.T, role string, tools []string) {
	t.Helper()

	fakeGHPath := writeFakeGH(t)

	// Override ghShimCreator so we use our fake gh as the "real" gh.
	orig := ghShimCreator
	ghShimCreator = func() (string, func()) {
		shimDir, cleanup, err := createGHShimWithPath(fakeGHPath)
		if err != nil {
			t.Errorf("createGHShimWithPath: %v", err)
			return "", func() {}
		}
		return shimDir, cleanup
	}
	t.Cleanup(func() { ghShimCreator = orig })

	cfg := defaultRoleConfig(t, role)
	cfg.ToolAllowlist = tools

	// The "pi" script tries to run gh directly; its stderr is what we capture.
	// We write stderr to a sidecar file so we can assert the guard message.
	sidecarPath := filepath.Join(t.TempDir(), "gh-stderr.txt")
	scriptContent := "gh --version 2>" + sidecarPath + "\necho done\n"
	scriptPath := writeScript(t, scriptContent)

	var capturedArgs []string
	fakeCommandFactory(t, scriptPath, &capturedArgs)

	ctx := context.Background()
	exitCode, _, err := RunRole(ctx, cfg)
	if err != nil {
		t.Fatalf("RunRole failed: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("RunRole exit code: got %d, want 0", exitCode)
	}

	ghStderr, readErr := os.ReadFile(sidecarPath)
	if readErr != nil {
		t.Fatalf("sidecar file not created — gh was not invoked or script did not run: %v", readErr)
	}
	if !strings.Contains(string(ghStderr), "golemic guard") {
		t.Errorf("expected golemic guard message in gh stderr for role %q, got: %q", role, string(ghStderr))
	}
}

// TestGHGuard_GolemicInAgentEnvPasses verifies that when the shim is active,
// a subprocess with GOLEMIC_GH_AUTHORIZED=1 (as set by osExecutor) can still
// invoke gh — proving the golemic→gh internal path is not broken.
func TestGHGuard_GolemicInAgentEnvPasses(t *testing.T) {
	fakeGHPath := writeFakeGH(t)

	shimDir, cleanup, err := createGHShimWithPath(fakeGHPath)
	if err != nil {
		t.Fatalf("createGHShimWithPath: %v", err)
	}
	defer cleanup()

	// Simulate what osExecutor does: GOLEMIC_GH_AUTHORIZED=1 is set, shim is on PATH.
	cmd := exec.Command("sh", "-c", "gh --version")
	cmd.Env = append(filterEnv(os.Environ(), "PATH", "GOLEMIC_GH_AUTHORIZED"),
		"PATH="+shimDir+string(filepath.ListSeparator)+filepath.Dir(fakeGHPath),
		"GOLEMIC_GH_AUTHORIZED=1",
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		t.Fatalf("golemic internal gh call should succeed with GOLEMIC_GH_AUTHORIZED=1: %v", err)
	}
	if !strings.Contains(stdout.String(), "fake-gh") {
		t.Errorf("expected fake-gh output, got: %q", stdout.String())
	}
}

// TestGHGuard_NewPiCmdShimPrependedToPath verifies that newPiCmd puts the shim
// directory before the golemic binary dir in the agent PATH, so the shim takes
// precedence over any system gh.
func TestGHGuard_NewPiCmdShimPrependedToPath(t *testing.T) {
	fakeGHPath := writeFakeGH(t)

	shimDir, cleanup, err := createGHShimWithPath(fakeGHPath)
	if err != nil {
		t.Fatalf("createGHShimWithPath: %v", err)
	}
	defer cleanup()

	cfg := defaultRoleConfig(t, "dev")
	golemicDir := "/usr/local/bin"

	stdoutPath := filepath.Join(t.TempDir(), "out.txt")
	stderrPath := filepath.Join(t.TempDir(), "err.txt")
	stdoutFile, _ := os.Create(stdoutPath)
	stderrFile, _ := os.Create(stderrPath)
	defer stdoutFile.Close() //nolint:errcheck
	defer stderrFile.Close() //nolint:errcheck

	CommandFactory = func(name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}
	t.Cleanup(func() { CommandFactory = exec.Command })

	cmd := newPiCmd(cfg, nil, golemicDir, shimDir, stdoutFile, stderrFile)

	// newPiCmd appends its custom PATH at the end of cmd.Env (after os.Environ()).
	// We find the LAST PATH entry, which is the one we injected.
	var agentPath string
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "PATH=") {
			agentPath = strings.TrimPrefix(e, "PATH=")
		}
	}

	if agentPath == "" {
		t.Fatal("no PATH entry found in cmd.Env")
	}
	parts := strings.Split(agentPath, string(filepath.ListSeparator))
	if len(parts) < 2 {
		t.Fatalf("agent PATH too short: %q", agentPath)
	}
	if parts[0] != shimDir {
		t.Errorf("agent PATH[0] = %q, want shimDir %q", parts[0], shimDir)
	}
	if parts[1] != golemicDir {
		t.Errorf("agent PATH[1] = %q, want golemicDir %q", parts[1], golemicDir)
	}
}

// filterEnv returns a copy of env with all entries whose key equals any of the
// given names removed. Used to build a clean subprocess env in tests.
func filterEnv(env []string, keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			out = append(out, e)
			continue
		}
		if !drop[e[:idx]] {
			out = append(out, e)
		}
	}
	return out
}
