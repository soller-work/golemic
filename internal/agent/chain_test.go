package agent

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// captureArgsFactory returns a factory that records the full argv per invocation
// alongside running the given scripts in order.
func captureArgsFactory(t *testing.T, scripts []string, allArgs *[][]string) {
	t.Helper()
	invocations := 0
	CommandFactory = func(name string, args ...string) *exec.Cmd {
		idx := invocations
		invocations++
		*allArgs = append(*allArgs, append([]string{name}, args...))
		if idx >= len(scripts) {
			t.Fatalf("unexpected invocation %d (only %d scripts configured)", idx+1, len(scripts))
		}
		cmd := exec.Command(scripts[idx], args...)
		return cmd
	}
	t.Cleanup(func() { CommandFactory = exec.Command })
}

// limitErrorTranscript is a Pi JSONL transcript simulating a subscription limit error.
const limitErrorTranscript = `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"You have hit your limit today"}}` + "\n"

// stopTranscript is a Pi JSONL transcript simulating a successful run.
const stopTranscript = `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","errorMessage":""}}` + "\n"

// autoRetryEndFailTranscript simulates Pi declaring transient exhaustion.
const autoRetryEndFailTranscript = `{"type":"auto_retry_end","success":false,"attempt":3}` + "\n"

// taskFailTranscript simulates a non-zero exit from a task failure (no fallback signal).
// The process itself exits 1 via the script; stopReason is "stop" so no semantic failure.
const taskFailTranscript = `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","errorMessage":""}}` + "\n"

// abortedTranscript simulates a terminal stopReason:aborted at exit 0 (BR-4).
const abortedTranscript = `{"type":"message_end","message":{"role":"assistant","stopReason":"aborted","errorMessage":""}}` + "\n"

// errorNoLimitTranscript simulates stopReason:error without "limit" at exit 0 (BR-4).
const errorNoLimitTranscript = `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"internal server error"}}` + "\n"

// TestRunRole_SingleModel_Success verifies single-model configs are unchanged.
func TestRunRole_SingleModel_Success(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	scripts := []string{
		writeScript(t, `echo '`+stopTranscript[:len(stopTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}
	if len(allArgs) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(allArgs))
	}
	// Single model in args
	args := strings.Join(allArgs[0], " ")
	if !strings.Contains(args, "--model z-ai/glm-4.6") {
		t.Errorf("expected --model z-ai/glm-4.6 in args: %q", args)
	}
}

// TestRunRole_TwoModelChain_FirstLimitFallback verifies limit errors trigger fallback.
func TestRunRole_TwoModelChain_FirstLimitFallback(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = "model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	scripts := []string{
		writeScript(t, `echo '`+limitErrorTranscript[:len(limitErrorTranscript)-1]+`'; exit 0`),
		writeScript(t, `echo '`+stopTranscript[:len(stopTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code after fallback success: got %d, want 0", exitCode)
	}
	if len(allArgs) != 2 {
		t.Fatalf("expected 2 invocations (fallback used), got %d", len(allArgs))
	}
	if !strings.Contains(strings.Join(allArgs[0], " "), "--model model-a") {
		t.Errorf("first invocation should use model-a, got: %v", allArgs[0])
	}
	if !strings.Contains(strings.Join(allArgs[1], " "), "--model model-b") {
		t.Errorf("second invocation should use model-b, got: %v", allArgs[1])
	}
}

// TestRunRole_TwoModelChain_BothExhausted verifies exhausted chains return ErrModelChainExhausted
// and non-zero exit code.
func TestRunRole_TwoModelChain_BothExhausted(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = "model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	scripts := []string{
		writeScript(t, `echo '`+limitErrorTranscript[:len(limitErrorTranscript)-1]+`'; exit 0`),
		writeScript(t, `echo '`+limitErrorTranscript[:len(limitErrorTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if !errors.Is(err, ErrModelChainExhausted) {
		t.Fatalf("expected ErrModelChainExhausted, got err=%v exitCode=%d", err, exitCode)
	}
	if exitCode != 1 {
		t.Errorf("exit code after exhaustion: got %d, want 1", exitCode)
	}
	var chainErr *ModelChainExhaustedError
	if !errors.As(err, &chainErr) {
		t.Fatalf("error should be *ModelChainExhaustedError")
	}
	if len(chainErr.Attempts) != 2 {
		t.Errorf("expected 2 attempts recorded, got %d", len(chainErr.Attempts))
	}
	if chainErr.Attempts[0].Model != "model-a" {
		t.Errorf("attempts[0].Model = %q, want model-a", chainErr.Attempts[0].Model)
	}
	if chainErr.Attempts[1].Model != "model-b" {
		t.Errorf("attempts[1].Model = %q, want model-b", chainErr.Attempts[1].Model)
	}
	if chainErr.Attempts[0].Reason != "provider limit" {
		t.Errorf("attempts[0].Reason = %q, want 'provider limit'", chainErr.Attempts[0].Reason)
	}
}

// TestRunRole_AutoRetryEndFallback verifies auto_retry_end success=false triggers fallback.
func TestRunRole_AutoRetryEndFallback(t *testing.T) {
	cfg := defaultRoleConfig(t, "reviewer")
	cfg.Model = "model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	scripts := []string{
		writeScript(t, `echo '`+autoRetryEndFailTranscript[:len(autoRetryEndFailTranscript)-1]+`'; exit 0`),
		writeScript(t, `echo '`+stopTranscript[:len(stopTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}
	if len(allArgs) != 2 {
		t.Fatalf("expected 2 invocations, got %d", len(allArgs))
	}
}

// TestRunRole_NonFallbackTaskFailure verifies non-fallback failures don't try next model.
func TestRunRole_NonFallbackTaskFailure(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = "model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	// First model: non-zero exit, transcript has stopReason:stop (not a provider error)
	scripts := []string{
		writeScript(t, `echo '`+taskFailTranscript[:len(taskFailTranscript)-1]+`'; exit 1`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}
	if len(allArgs) != 1 {
		t.Fatalf("expected 1 invocation (no fallback on task failure), got %d", len(allArgs))
	}
}

// TestRunRole_EmptyModelChain verifies empty chain is an error.
func TestRunRole_EmptyModelChain(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = ",,,"

	_, _, err := RunRole(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for empty model chain")
	}
	if !strings.Contains(err.Error(), "model chain is empty") {
		t.Errorf("expected 'model chain is empty' in error, got: %v", err)
	}
}

// TestRunRole_ModelChainDeduplication verifies duplicates are dropped.
func TestRunRole_ModelChainDeduplication(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = "model-a, model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	// Should only be invoked twice (deduplicated chain: model-a, model-b),
	// but first model succeeds so only one invocation.
	scripts := []string{
		writeScript(t, `echo '`+stopTranscript[:len(stopTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exit code: got %d, want 0", exitCode)
	}
	if len(allArgs) != 1 {
		t.Fatalf("deduplicated chain: expected 1 invocation, got %d", len(allArgs))
	}
}

// TestRunRole_SemanticFailureAbortedAtExitZero verifies that stopReason:aborted at exit 0
// is reported as a non-zero role result and does not trigger fallback (BR-4).
func TestRunRole_SemanticFailureAbortedAtExitZero(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = "model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	scripts := []string{
		writeScript(t, `echo '`+abortedTranscript[:len(abortedTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for stopReason:aborted at exit 0, got 0")
	}
	if len(allArgs) != 1 {
		t.Fatalf("expected 1 invocation (no fallback on aborted), got %d", len(allArgs))
	}
}

// TestRunRole_SemanticFailureErrorNoLimitAtExitZero verifies that stopReason:error without
// "limit" in errorMessage at exit 0 is reported as a non-zero role result and does not
// trigger fallback (BR-4).
func TestRunRole_SemanticFailureErrorNoLimitAtExitZero(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = "model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	scripts := []string{
		writeScript(t, `echo '`+errorNoLimitTranscript[:len(errorNoLimitTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	exitCode, _, err := RunRole(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for stopReason:error (no limit) at exit 0, got 0")
	}
	if len(allArgs) != 1 {
		t.Fatalf("expected 1 invocation (no fallback on non-limit error), got %d", len(allArgs))
	}
}

// TestRunRole_EachModelGetsExactlyOneModelArg verifies BR-3.
func TestRunRole_EachModelGetsExactlyOneModelArg(t *testing.T) {
	cfg := defaultRoleConfig(t, "dev")
	cfg.Model = "model-a, model-b"
	cfg.Timeout = 5 * time.Second

	var allArgs [][]string
	scripts := []string{
		writeScript(t, `echo '`+limitErrorTranscript[:len(limitErrorTranscript)-1]+`'; exit 0`),
		writeScript(t, `echo '`+stopTranscript[:len(stopTranscript)-1]+`'; exit 0`),
	}
	captureArgsFactory(t, scripts, &allArgs)

	_, _, _ = RunRole(context.Background(), cfg)

	for i, args := range allArgs {
		modelCount := 0
		for j, a := range args {
			if a == "--model" && j+1 < len(args) {
				modelCount++
			}
		}
		if modelCount != 1 {
			t.Errorf("invocation %d: expected exactly 1 --model arg, got %d; args: %v", i, modelCount, args)
		}
	}
}
