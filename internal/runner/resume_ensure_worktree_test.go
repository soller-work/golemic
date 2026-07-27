package runner

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/config"
	"golemic/internal/eventlog"
)

// recordingEventWriter captures worktree_created (and any) events in memory.
type recordingEventWriter struct{ events []eventlog.Event }

func (w *recordingEventWriter) Write(ev eventlog.Event) error {
	w.events = append(w.events, ev)
	return nil
}

// newEnsureWTRunner builds a minimal Runner for ensureDevWorktreeForResume tests.
func newEnsureWTRunner(t *testing.T, exec *fakeExecutor) (*Runner, *bytes.Buffer) {
	f := newRunnerFixture(t,
		withExecutor(exec),
		withHomeDir(t.TempDir()),
		withRepoRoot("/repo"),
		withProject("proj"),
		withIssueNum(7),
		withConfig(&config.Config{Project: "proj"}),
		withBranchName("golemic/issue-7"),
		withRunID("run-1"),
	)
	return f.r, f.stderr
}

func devWTFor(r *Runner) string {
	return filepath.Join(r.homeDir, ".golemic", r.cfg.Project, "worktrees", "issue-7")
}

// existing valid dev worktree on the correct branch -> idempotent no-op.
func TestEnsureDevWorktreeForResume_IdempotentNoOp(t *testing.T) {
	var r *Runner
	exec := &fakeExecutor{}
	r, _ = newEnsureWTRunner(t, exec)
	devWT := devWTFor(r)
	exec.runFunc = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "worktree list --porcelain") {
			return "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n" +
				"worktree " + devWT + "\nHEAD bbb\nbranch refs/heads/golemic/issue-7\n\n", nil
		}
		return "", errors.New("unexpected call: git " + joined)
	}

	writer := &recordingEventWriter{}
	if o := r.ensureDevWorktreeForResume(writer); o != "" {
		t.Fatalf("expected success (no-op), got %q", o)
	}
	for _, c := range exec.calls {
		if len(c.args) > 0 && c.args[len(c.args)-1] == "origin" && contains(c.args, "fetch") {
			t.Fatalf("unexpected fetch: worktree recreation must not run when already registered")
		}
	}
	if len(writer.events) != 0 {
		t.Fatalf("expected no worktree_created event on no-op, got %d", len(writer.events))
	}
}

// directory registered on a different branch -> clear abort, no recreation.
func TestEnsureDevWorktreeForResume_WrongBranchAborts(t *testing.T) {
	exec := &fakeExecutor{}
	r, stderr := newEnsureWTRunner(t, exec)
	devWT := devWTFor(r)
	exec.runFunc = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "worktree list --porcelain") {
			return "worktree " + devWT + "\nHEAD bbb\nbranch refs/heads/golemic/issue-999\n\n", nil
		}
		return "", errors.New("unexpected call: git " + joined)
	}

	if o := r.ensureDevWorktreeForResume(&recordingEventWriter{}); o != outcomeAborted {
		t.Fatalf("expected outcomeAborted, got %q", o)
	}
	if !strings.Contains(stderr.String(), "registered on branch") {
		t.Fatalf("expected clear wrong-branch error, got: %s", stderr.String())
	}
}

// no local dev worktree -> recreated on the PR branch before delegating.
func TestEnsureDevWorktreeForResume_RecreatesFromRemoteBranch(t *testing.T) {
	exec := &fakeExecutor{}
	r, _ := newEnsureWTRunner(t, exec)
	exec.runFunc = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n", nil
		case strings.Contains(joined, "ls-remote"):
			return "deadbeef\trefs/heads/golemic/issue-7\n", nil
		default: // fetch, worktree add, config *
			return "", nil
		}
	}

	writer := &recordingEventWriter{}
	if o := r.ensureDevWorktreeForResume(writer); o != "" {
		t.Fatalf("expected success, got %q", o)
	}

	if !hasTrackingWorktreeAdd(exec.calls, "golemic/issue-7") {
		t.Fatalf("expected a tracking `git worktree add --track -b golemic/issue-7` call")
	}
	if len(writer.events) != 1 || writer.events[0].Type != eventlog.EventWorktreeCreated {
		t.Fatalf("expected one worktree_created event, got %+v", writer.events)
	}
}

// hasTrackingWorktreeAdd reports whether a `git worktree add --track -b <branch>` call
// was recorded.
func hasTrackingWorktreeAdd(calls []callRecord, branch string) bool {
	for _, c := range calls {
		if c.name == "git" && contains(c.args, "worktree") && contains(c.args, "add") &&
			contains(c.args, "--track") && contains(c.args, branch) {
			return true
		}
	}
	return false
}

// remote branch missing during recreation -> abort, no event.
func TestEnsureDevWorktreeForResume_RemoteBranchMissingAborts(t *testing.T) {
	exec := &fakeExecutor{}
	r, stderr := newEnsureWTRunner(t, exec)
	exec.runFunc = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree list --porcelain"):
			return "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n", nil
		case strings.Contains(joined, "ls-remote"):
			return "", errors.New("exit status 2")
		default:
			return "", nil
		}
	}

	writer := &recordingEventWriter{}
	if o := r.ensureDevWorktreeForResume(writer); o != outcomeAborted {
		t.Fatalf("expected outcomeAborted, got %q", o)
	}
	if !strings.Contains(stderr.String(), "REMOTE_BRANCH_NOT_FOUND") {
		t.Fatalf("expected REMOTE_BRANCH_NOT_FOUND message, got: %s", stderr.String())
	}
	if len(writer.events) != 0 {
		t.Fatalf("expected no worktree_created event on failure, got %d", len(writer.events))
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
