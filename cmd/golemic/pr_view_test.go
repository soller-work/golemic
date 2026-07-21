package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// AC: --pr is required and must be a positive integer.
func TestRunPRView_MissingPRFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPRView([]string{"golemic", "pr-view"}, &stdout, &stderr, fakeExecutor{})
	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--pr") {
		t.Errorf("stderr should mention --pr, got: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on error, got: %q", stdout.String())
	}
}

func TestRunPRView_ZeroPRFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPRView([]string{"golemic", "pr-view", "--pr", "0"}, &stdout, &stderr, fakeExecutor{})
	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--pr") {
		t.Errorf("stderr should mention --pr, got: %q", stderr.String())
	}
}

func TestRunPRView_NegativePRFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPRView([]string{"golemic", "pr-view", "--pr", "-5"}, &stdout, &stderr, fakeExecutor{})
	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
}

// AC: valid PR number calls gh pr view and prints output to stdout.
func TestRunPRView_Success(t *testing.T) {
	const prOutput = "title:\tFix the bug\nstate:\tOPEN\n"
	exec := fakeExecutor{
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "view" && args[2] == "42" {
				return prOutput, nil
			}
			return "", fmt.Errorf("unexpected call: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	code := runPRView([]string{"golemic", "pr-view", "--pr", "42"}, &stdout, &stderr, exec)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0; stderr=%q", code, stderr.String())
	}
	if stdout.String() != prOutput {
		t.Errorf("stdout = %q, want %q", stdout.String(), prOutput)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty on success, got: %q", stderr.String())
	}
}

// AC: gh failure surfaces on stderr with non-zero exit.
func TestRunPRView_GHFailure(t *testing.T) {
	exec := fakeExecutor{
		runWithEnvFunc: func(_ map[string]string, name string, args ...string) (string, error) {
			if name == "gh" {
				return "", fmt.Errorf("gh: could not resolve to a Repository")
			}
			return "", fmt.Errorf("unexpected: %s %v", name, args)
		},
	}

	var stdout, stderr bytes.Buffer
	code := runPRView([]string{"golemic", "pr-view", "--pr", "99"}, &stdout, &stderr, exec)
	if code != 1 {
		t.Errorf("exit code: got %d, want 1", code)
	}
	if stderr.Len() == 0 {
		t.Error("stderr should contain the error message")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on error, got: %q", stdout.String())
	}
}
