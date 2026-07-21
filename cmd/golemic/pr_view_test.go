package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/config"
)

func prViewFixture(t *testing.T, project, reviewerToken string) (homeDir, repoRoot string) {
	t.Helper()
	homeDir = t.TempDir()
	repoRoot = t.TempDir()

	cfgDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{"project":%q,"verify_command":"go test"}`, project)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	credsDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		t.Fatal(err)
	}
	credsJSON := fmt.Sprintf(`{"dev_token":"ghp_dev_test","reviewer_token":%q}`, reviewerToken)
	if err := os.WriteFile(filepath.Join(credsDir, "credentials.json"), []byte(credsJSON), 0600); err != nil {
		t.Fatal(err)
	}

	return homeDir, repoRoot
}

func setPRViewEnv(t *testing.T, homeDir string) {
	t.Helper()
	origHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", homeDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	for _, key := range []string{"GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN"} {
		orig := os.Getenv(key)
		_ = os.Unsetenv(key)
		k, v := key, orig
		t.Cleanup(func() { _ = os.Setenv(k, v) })
	}
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
}

func successPRViewExecutor(repoRoot, token string, called *bool) fakeExecutor {
	return fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" && len(args) >= 1 && args[0] == "rev-parse" {
				return repoRoot + "\n", nil
			}
			return "", fmt.Errorf("unexpected Run: %s %v", name, args)
		},
		runWithEnvInDirFunc: func(env map[string]string, dir string, name string, args ...string) (string, error) {
			*called = true
			if name != "gh" {
				return "", fmt.Errorf("unexpected command: %s %v", name, args)
			}
			if dir != repoRoot {
				return "", fmt.Errorf("unexpected dir: %s", dir)
			}
			if got := env["GH_TOKEN"]; got != token {
				return "", fmt.Errorf("unexpected GH_TOKEN: %q", got)
			}
			if strings.Join(args, " ") != "pr view 123" {
				return "", fmt.Errorf("unexpected gh args: %v", args)
			}
			return "PR #123\nTitle: Fix bug\n", nil
		},
	}
}

func TestRunPRView_Success(t *testing.T) {
	homeDir, repoRoot := prViewFixture(t, "testproject", "ghp_reviewer_test")
	setPRViewEnv(t, homeDir)
	chdirForTest(t, repoRoot)

	called := false
	exec := successPRViewExecutor(repoRoot, "ghp_reviewer_test", &called)

	var stdout, stderr bytes.Buffer
	code := runPRView([]string{"golemic", "pr-view", "--pr", "123"}, &stdout, &stderr, exec, config.Load)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr=%s", code, stderr.String())
	}
	if !called {
		t.Fatal("gh pr view was not called")
	}
	if got := stdout.String(); got != "PR #123\nTitle: Fix bug\n" {
		t.Errorf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", stderr.String())
	}
}

func TestRunPRView_ValidationFailures(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantError string
	}{
		{name: "missing pr flag", args: []string{"golemic", "pr-view"}, wantError: "--pr must be a positive integer"},
		{name: "zero pr flag", args: []string{"golemic", "pr-view", "--pr", "0"}, wantError: "--pr must be a positive integer"},
		{name: "negative pr flag", args: []string{"golemic", "pr-view", "--pr", "-5"}, wantError: "--pr must be a positive integer"},
		{name: "non-numeric pr flag", args: []string{"golemic", "pr-view", "--pr", "abc"}, wantError: "invalid value"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exec := fakeExecutor{
				runFunc: func(name string, args ...string) (string, error) {
					return "", fmt.Errorf("unexpected Run: %s %v", name, args)
				},
				runWithEnvInDirFunc: func(env map[string]string, dir string, name string, args ...string) (string, error) {
					return "", fmt.Errorf("unexpected RunWithEnvInDir: %s %v", name, args)
				},
			}

			code := runPRView(tc.args, &stdout, &stderr, exec, config.Load)
			if code != 1 {
				t.Fatalf("exit code: got %d, want 1", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout should be empty, got %q", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.wantError) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tc.wantError)
			}
		})
	}
}
