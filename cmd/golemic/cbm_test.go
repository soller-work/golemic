package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeNpx installs a fake npx script on PATH and restores the original factory after the test.
func withFakeNpx(t *testing.T, script string) (recordFile string) {
	t.Helper()
	tmpDir := t.TempDir()
	recordFile = filepath.Join(tmpDir, "npx.calls")

	// Write the fake npx shell script.
	fakePath := filepath.Join(tmpDir, "npx")
	content := "#!/bin/sh\necho \"$@\" >> " + recordFile + "\n" + script + "\n"
	if err := os.WriteFile(fakePath, []byte(content), 0755); err != nil {
		t.Fatalf("write fake npx: %v", err)
	}

	origFactory := cbmCommandFactory
	cbmCommandFactory = func(name string, args ...string) *exec.Cmd {
		if name == "npx" {
			cmd := exec.Command(fakePath, args...)
			return cmd
		}
		return exec.Command(name, args...)
	}
	t.Cleanup(func() { cbmCommandFactory = origFactory })
	return recordFile
}

func TestCBM_NoArgs_PrintsUsage(t *testing.T) {
	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm"}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Error("expected non-zero exit for missing sub")
	}
	if !strings.Contains(stderr.String(), "Allowed") {
		t.Errorf("expected allowed subs in stderr; got: %s", stderr.String())
	}
}

func TestCBM_UnknownSub_RejectsWithoutNpx(t *testing.T) {
	recordFile := withFakeNpx(t, "exit 0")

	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "bogus_sub"}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Error("expected non-zero exit for unknown sub")
	}
	if _, err := os.Stat(recordFile); err == nil {
		calls, _ := os.ReadFile(recordFile)
		t.Errorf("npx must not be invoked for unknown sub; got calls: %s", calls)
	}
	if !strings.Contains(stderr.String(), "bogus_sub") {
		t.Errorf("expected unknown sub name in stderr; got: %s", stderr.String())
	}
	// BR-C3: stderr must list the eight allowed subs
	for _, sub := range cbmAllowedSubs {
		if !strings.Contains(stderr.String(), sub) {
			t.Errorf("stderr missing allowed sub %q; got: %s", sub, stderr.String())
		}
	}
}

func TestCBM_IndexRepositoryRejected(t *testing.T) {
	recordFile := withFakeNpx(t, "exit 0")

	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "index_repository"}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Error("expected non-zero exit for index_repository (write sub, runner-internal)")
	}
	if _, err := os.Stat(recordFile); err == nil {
		t.Error("npx must not be invoked for index_repository")
	}
}

func TestCBM_AllowedSubs_ForwardArgv(t *testing.T) {
	for _, sub := range cbmAllowedSubs {
		sub := sub
		t.Run(sub, func(t *testing.T) {
			recordFile := withFakeNpx(t, "exit 0")

			var stdout, stderr bytes.Buffer
			code := runCBM([]string{"golemic", "cbm", sub, "--extra", "arg"}, &stdout, &stderr)
			if code != 0 {
				t.Errorf("expected exit 0 for sub %q; stderr: %s", sub, stderr.String())
			}

			callsRaw, err := os.ReadFile(recordFile)
			if err != nil {
				t.Fatalf("fake npx never called for sub %q", sub)
			}
			calls := string(callsRaw)
			// Verify the npx argv: -y codebase-memory-mcp@0.9.0 cli <sub> --extra arg
			for _, want := range []string{"-y", "codebase-memory-mcp@0.9.0", "cli", sub, "--extra", "arg"} {
				if !strings.Contains(calls, want) {
					t.Errorf("sub %q: npx argv missing %q; got: %s", sub, want, calls)
				}
			}
		})
	}
}

func TestCBM_ForwardsNonZeroExit(t *testing.T) {
	withFakeNpx(t, "exit 42")

	var stderr bytes.Buffer
	code := runCBM([]string{"golemic", "cbm", "search_graph", "query"}, &bytes.Buffer{}, &stderr)
	if code != 42 {
		t.Errorf("expected exit 42 forwarded from npx; got %d", code)
	}
}
