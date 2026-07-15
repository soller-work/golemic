package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golemic/internal/eventlog"
)

// ---------------------------------------------------------------------------
// mockExecutor — records all command calls for test assertions
// ---------------------------------------------------------------------------

type cmdCall struct {
	Env  map[string]string // nil for Run, non-nil for RunWithEnv
	Name string
	Args []string
}

type mockExecutor struct {
	mu        sync.Mutex
	calls     []cmdCall
	index     int // for sequenced responses
	responses []execResponse
}

type execResponse struct {
	Stdout string
	Err    error
}

func newMockExecutor(responses ...execResponse) *mockExecutor {
	return &mockExecutor{responses: responses}
}

func (m *mockExecutor) Run(name string, args ...string) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, cmdCall{Name: name, Args: args})
	idx := m.index
	if idx < len(m.responses) {
		m.index++
	}
	m.mu.Unlock()

	if idx >= len(m.responses) {
		return "", nil
	}
	return m.responses[idx].Stdout, m.responses[idx].Err
}

func (m *mockExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, cmdCall{Env: env, Name: name, Args: args})
	idx := m.index
	if idx < len(m.responses) {
		m.index++
	}
	m.mu.Unlock()

	if idx >= len(m.responses) {
		return "", nil
	}
	return m.responses[idx].Stdout, m.responses[idx].Err
}

func (m *mockExecutor) Calls() []cmdCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]cmdCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// ---------------------------------------------------------------------------
// mockEventWriter — records events in memory
// ---------------------------------------------------------------------------

type mockEventWriter struct {
	mu     sync.Mutex
	events []eventlog.Event
}

func newMockEventWriter() *mockEventWriter {
	return &mockEventWriter{}
}

func (w *mockEventWriter) Write(ev eventlog.Event) error {
	w.mu.Lock()
	w.events = append(w.events, ev)
	w.mu.Unlock()
	return nil
}

func (w *mockEventWriter) Events() []eventlog.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]eventlog.Event, len(w.events))
	copy(cp, w.events)
	return cp
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

const testBaseSha = "abc123def456abc123def456abc123def456abc1"

// defaultRepoRoot is the repoRoot returned by testCreateArgs.
const defaultRepoRoot = "/tmp/test-repo"

func testCreateArgs() (repoRoot, golemicDir, runID string, issueNumber int, botLogin string) {
	return defaultRepoRoot,
		filepath.Join("/home/testuser", ".golemic", "test-project"),
		"run-001",
		42,
		"golem-dev-bot"
}

// expectedWorktreePath returns the expected worktree path for test issue 42.
func expectedWorktreePath(golemicDir string) string {
	return filepath.Join(golemicDir, "worktrees", "issue-42")
}

// expectedBranch returns the expected branch name for test issue 42.
func expectedBranch() string {
	return "golemic/issue-42"
}

// defaultSuccessResponses provides mock responses for a successful Create call.
// Order: fetch, rev-parse, worktree add, config credential.helper, config user.name, config user.email.
func defaultSuccessResponses() []execResponse {
	return []execResponse{
		{Stdout: "", Err: nil},                               // git -C <repoRoot> fetch origin
		{Stdout: testBaseSha + "\n", Err: nil},               // git -C <repoRoot> rev-parse origin/main
		{Stdout: "Created worktree\n", Err: nil},             // git -C <repoRoot> worktree add
		{Stdout: "", Err: nil},                               // git -C <wtPath> config credential.helper
		{Stdout: "", Err: nil},                               // git -C <wtPath> config user.name
		{Stdout: "", Err: nil},                               // git -C <wtPath> config user.email
	}
}

// verifyNoCleanup asserts that calls contains no cleanup commands
// (git worktree remove or git branch -D).
func verifyNoCleanup(t testing.TB, calls []cmdCall) {
	t.Helper()
	for _, call := range calls {
		// After P1 all host-repo commands use git -C <repoRoot> ...,
		// so cleanup commands look like:
		//   git -C <repoRoot> worktree remove <path>
		//   git -C <repoRoot> branch -D <branch>
		hasWorktreeRemove := call.Name == "git" &&
			len(call.Args) >= 4 &&
			call.Args[2] == "worktree" &&
			call.Args[3] == "remove"
		if hasWorktreeRemove {
			t.Error("git worktree remove was called — cleanup must not run on error")
		}

		hasBranchDelete := call.Name == "git" &&
			len(call.Args) >= 4 &&
			call.Args[2] == "branch" &&
			call.Args[3] == "-D"
		if hasBranchDelete {
			t.Error("git branch -D was called — cleanup must not run on error")
		}
	}
}

// expectCall asserts that the cmdCall matches the expected invocation.
func expectCall(t *testing.T, call cmdCall, envMarker, name string, args ...string) {
	t.Helper()
	if call.Name != name {
		t.Errorf("expected command name %q, got %q", name, call.Name)
	}
	if len(call.Args) != len(args) {
		t.Errorf("expected %d args, got %d: %v", len(args), len(call.Args), call.Args)
	} else {
		for i := range args {
			if call.Args[i] != args[i] {
				t.Errorf("arg[%d]: expected %q, got %q", i, args[i], call.Args[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// AC-001: Worktree created with correct git command sequence
// ---------------------------------------------------------------------------

func TestCreate_GitCommandSequence(t *testing.T) {
	mockExec := newMockExecutor(defaultSuccessResponses()...)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()
	wtPath := expectedWorktreePath(golemicDir)

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	calls := mockExec.Calls()
	if len(calls) < 6 {
		t.Fatalf("expected at least 6 executor calls, got %d", len(calls))
	}

	// Call 0: git -C <repoRoot> fetch origin
	expectCall(t, calls[0], "", "git", "-C", defaultRepoRoot, "fetch", "origin")

	// Call 1: git -C <repoRoot> rev-parse origin/main
	expectCall(t, calls[1], "", "git", "-C", defaultRepoRoot, "rev-parse", "origin/main")

	// Call 2: git -C <repoRoot> worktree add <path> -b <branch> origin/main
	expectCall(t, calls[2], "", "git", "-C", defaultRepoRoot, "worktree", "add", wtPath, "-b", expectedBranch(), "origin/main")

	// Call 3: git -C <wtPath> config credential.helper
	credHelper := "!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f"
	expectCall(t, calls[3], "", "git", "-C", wtPath, "config", "credential.helper", credHelper)

	// Call 4: git -C <wtPath> config user.name
	expectCall(t, calls[4], "", "git", "-C", wtPath, "config", "user.name", botLogin)

	// Call 5: git -C <wtPath> config user.email
	expectCall(t, calls[5], "", "git", "-C", wtPath, "config", "user.email", botLogin)
}

// ---------------------------------------------------------------------------
// AC-002: worktree_created event contains correct fields
// ---------------------------------------------------------------------------

func TestCreate_EventWritten(t *testing.T) {
	mockExec := newMockExecutor(defaultSuccessResponses()...)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()
	wtPath := expectedWorktreePath(golemicDir)

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	events := eventWriter.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]
	if ev.Type != eventlog.EventWorktreeCreated {
		t.Errorf("event type: got %q, want %q", ev.Type, eventlog.EventWorktreeCreated)
	}
	if ev.RunID != runID {
		t.Errorf("event runId: got %q, want %q", ev.RunID, runID)
	}

	// Parse payload
	var payload map[string]string
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal event payload: %v", err)
	}

	if payload["path"] != wtPath {
		t.Errorf("payload.path: got %q, want %q", payload["path"], wtPath)
	}
	if payload["branch"] != expectedBranch() {
		t.Errorf("payload.branch: got %q, want %q", payload["branch"], expectedBranch())
	}
	if payload["baseSha"] != testBaseSha {
		t.Errorf("payload.baseSha: got %q, want %q", payload["baseSha"], testBaseSha)
	}
	if payload["role"] != "dev" {
		t.Errorf("payload.role: got %q, want %q", payload["role"], "dev")
	}
}

// ---------------------------------------------------------------------------
// AC-003: Cleanup removes worktree and local branch
// ---------------------------------------------------------------------------

func TestCleanup_RemovesWorktreeAndBranch(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil}, // git -C <repoRoot> worktree remove
		execResponse{Stdout: "", Err: nil}, // git -C <repoRoot> branch -D
	)
	_, golemicDir, _, issueNum, _ := testCreateArgs()
	wtPath := expectedWorktreePath(golemicDir)

	err := Cleanup(defaultRepoRoot, golemicDir, issueNum, mockExec)
	if err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}

	calls := mockExec.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 executor calls, got %d", len(calls))
	}

	// Call 0: git -C <repoRoot> worktree remove <path>
	expectCall(t, calls[0], "", "git", "-C", defaultRepoRoot, "worktree", "remove", wtPath)

	// Call 1: git -C <repoRoot> branch -D <branch>
	expectCall(t, calls[1], "", "git", "-C", defaultRepoRoot, "branch", "-D", expectedBranch())
}

// ---------------------------------------------------------------------------
// AC-004: No cleanup on error during worktree creation
// ---------------------------------------------------------------------------

func TestCreate_NoCleanupOnError(t *testing.T) {
	// Mock executor that succeeds for fetch + rev-parse, then fails on worktree add.
	failResp := execResponse{Stdout: "", Err: errors.New("git worktree add failed: exit code 128")}
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil},                  // git -C <repoRoot> fetch origin
		execResponse{Stdout: testBaseSha + "\n", Err: nil},  // git -C <repoRoot> rev-parse
		failResp,                                             // git -C <repoRoot> worktree add → FAILS
	)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err == nil {
		t.Fatal("expected error from Create, got nil")
	}
	if !strings.Contains(err.Error(), "GIT_WORKTREE_ADD_FAILED") {
		t.Errorf("expected GIT_WORKTREE_ADD_FAILED in error, got: %v", err)
	}

	calls := mockExec.Calls()

	// Verify that only the first 3 calls happened (fetch, rev-parse, worktree add).
	if len(calls) != 3 {
		t.Fatalf("expected exactly 3 executor calls (no cleanup), got %d", len(calls))
	}

	// Verify no cleanup commands.
	verifyNoCleanup(t, calls)

	// Verify no event was written on failure.
	if events := eventWriter.Events(); len(events) != 0 {
		t.Errorf("expected 0 events on failure, got %d", len(events))
	}
}

// ---------------------------------------------------------------------------
// BR-005: No cleanup on any error after worktree exists (config failures)
// ---------------------------------------------------------------------------

// TestCreate_NoCleanupOn_ConfigCredHelperFails proves that when credential.helper
// config fails, the partial worktree is NOT cleaned up (BR-005).
func TestCreate_NoCleanupOn_ConfigCredHelperFails(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil},                  // git -C <repoRoot> fetch origin
		execResponse{Stdout: testBaseSha + "\n", Err: nil},  // git -C <repoRoot> rev-parse
		execResponse{Stdout: "OK\n", Err: nil},              // git -C <repoRoot> worktree add
		execResponse{Stdout: "", Err: errors.New("invalid key")}, // git config credential.helper → FAILS
	)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err == nil {
		t.Fatal("expected error from Create, got nil")
	}
	if !strings.Contains(err.Error(), "GIT_CONFIG_FAILED") {
		t.Errorf("expected GIT_CONFIG_FAILED in error, got: %v", err)
	}

	calls := mockExec.Calls()

	// Should have only 4 calls (fetch, rev-parse, worktree add, config cred helper).
	if len(calls) != 4 {
		t.Fatalf("expected exactly 4 executor calls (no cleanup), got %d", len(calls))
	}

	// No cleanup commands.
	verifyNoCleanup(t, calls)

	// No event written.
	if events := eventWriter.Events(); len(events) != 0 {
		t.Errorf("expected 0 events on failure, got %d", len(events))
	}
}

// TestCreate_NoCleanupOn_ConfigUserNameFails proves that when user.name config
// fails, the partial worktree is NOT cleaned up (BR-005).
func TestCreate_NoCleanupOn_ConfigUserNameFails(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil},                  // git -C <repoRoot> fetch origin
		execResponse{Stdout: testBaseSha + "\n", Err: nil},  // git -C <repoRoot> rev-parse
		execResponse{Stdout: "OK\n", Err: nil},              // git -C <repoRoot> worktree add
		execResponse{Stdout: "", Err: nil},                  // git config credential.helper → OK
		execResponse{Stdout: "", Err: errors.New("invalid user.name")}, // git config user.name → FAILS
	)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err == nil {
		t.Fatal("expected error from Create, got nil")
	}
	if !strings.Contains(err.Error(), "GIT_CONFIG_FAILED") {
		t.Errorf("expected GIT_CONFIG_FAILED in error, got: %v", err)
	}

	calls := mockExec.Calls()

	// Should have only 5 calls (fetch, rev-parse, worktree add, config cred, config name).
	if len(calls) != 5 {
		t.Fatalf("expected exactly 5 executor calls (no cleanup), got %d", len(calls))
	}

	verifyNoCleanup(t, calls)

	if events := eventWriter.Events(); len(events) != 0 {
		t.Errorf("expected 0 events on failure, got %d", len(events))
	}
}

// TestCreate_NoCleanupOn_ConfigUserEmailFails proves that when user.email config
// fails, the partial worktree is NOT cleaned up (BR-005).
func TestCreate_NoCleanupOn_ConfigUserEmailFails(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil},                  // git -C <repoRoot> fetch origin
		execResponse{Stdout: testBaseSha + "\n", Err: nil},  // git -C <repoRoot> rev-parse
		execResponse{Stdout: "OK\n", Err: nil},              // git -C <repoRoot> worktree add
		execResponse{Stdout: "", Err: nil},                  // git config credential.helper → OK
		execResponse{Stdout: "", Err: nil},                  // git config user.name → OK
		execResponse{Stdout: "", Err: errors.New("invalid user.email")}, // git config user.email → FAILS
	)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err == nil {
		t.Fatal("expected error from Create, got nil")
	}
	if !strings.Contains(err.Error(), "GIT_CONFIG_FAILED") {
		t.Errorf("expected GIT_CONFIG_FAILED in error, got: %v", err)
	}

	calls := mockExec.Calls()

	// Should have only 6 calls (fetch, rev-parse, worktree add, 3 configs).
	if len(calls) != 6 {
		t.Fatalf("expected exactly 6 executor calls (no cleanup), got %d", len(calls))
	}

	verifyNoCleanup(t, calls)

	if events := eventWriter.Events(); len(events) != 0 {
		t.Errorf("expected 0 events on failure, got %d", len(events))
	}
}

// ---------------------------------------------------------------------------
// Additional tests: error propagation, edge cases
// ---------------------------------------------------------------------------

func TestCreate_FetchFails(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: errors.New("network error")},
	)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "GIT_FETCH_FAILED") {
		t.Errorf("expected GIT_FETCH_FAILED in error, got: %v", err)
	}
}

func TestCreate_RevParseFails(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil},
		execResponse{Stdout: "", Err: errors.New("origin/main not found")},
	)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "GIT_REV_PARSE_FAILED") {
		t.Errorf("expected GIT_REV_PARSE_FAILED in error, got: %v", err)
	}
}

func TestCreate_EventWriteFails(t *testing.T) {
	mockExec := newMockExecutor(defaultSuccessResponses()[:6]...) // 6 successful responses
	// eventWriter that fails on Write
	failWriter := &failEventWriter{}
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, failWriter)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "EVENT_WRITE_FAILED") {
		t.Errorf("expected EVENT_WRITE_FAILED in error, got: %v", err)
	}
}

// failEventWriter returns an error on every Write call.
type failEventWriter struct{}

func (f *failEventWriter) Write(ev eventlog.Event) error {
	return fmt.Errorf("write error")
}

func TestCleanup_RemoveFails(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: errors.New("worktree not found")},
	)
	_, golemicDir, _, issueNum, _ := testCreateArgs()

	err := Cleanup(defaultRepoRoot, golemicDir, issueNum, mockExec)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "CLEANUP_REMOVE_FAILED") {
		t.Errorf("expected CLEANUP_REMOVE_FAILED in error, got: %v", err)
	}
}

func TestCleanup_BranchDeleteFails(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil},                                   // worktree remove succeeds
		execResponse{Stdout: "", Err: errors.New("branch does not exist")},   // branch -D fails
	)
	_, golemicDir, _, issueNum, _ := testCreateArgs()

	err := Cleanup(defaultRepoRoot, golemicDir, issueNum, mockExec)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "CLEANUP_BRANCH_FAILED") {
		t.Errorf("expected CLEANUP_BRANCH_FAILED in error, got: %v", err)
	}
}

// TestCreate_EventTimestamp — event should have a valid RFC3339 timestamp.
func TestCreate_EventTimestamp(t *testing.T) {
	mockExec := newMockExecutor(defaultSuccessResponses()...)
	eventWriter := newMockEventWriter()
	_, golemicDir, runID, issueNum, botLogin := testCreateArgs()

	err := Create(defaultRepoRoot, golemicDir, runID, issueNum, botLogin, mockExec, eventWriter)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	events := eventWriter.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	_, err = time.Parse(time.RFC3339, events[0].Ts)
	if err != nil {
		t.Errorf("event timestamp is not valid RFC3339: %q — %v", events[0].Ts, err)
	}
}

// TestCreate_RepoRootIsUsed — verifies that repoRoot is threaded into all
// host-repo git commands as `git -C <repoRoot>`.
func TestCreate_RepoRootIsUsed(t *testing.T) {
	distinctRepoRoot := "/repos/host-XYZ"
	mockExec := newMockExecutor(defaultSuccessResponses()...)
	eventWriter := newMockEventWriter()

	err := Create(distinctRepoRoot, "/tmp/.golemic/proj", "run-rr", 1, "bot", mockExec, eventWriter)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	calls := mockExec.Calls()
	if len(calls) < 1 {
		t.Fatal("expected at least 1 executor call")
	}
	// First git call should use the distinctive repoRoot.
	expectCall(t, calls[0], "", "git", "-C", distinctRepoRoot, "fetch", "origin")
}

// TestCleanup_RepoRootIsUsed — verifies that Cleanup threads repoRoot into
// git worktree remove as `git -C <repoRoot> worktree remove <path>`.
func TestCleanup_RepoRootIsUsed(t *testing.T) {
	distinctRepoRoot := "/repos/host-XYZ"
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: nil},
		execResponse{Stdout: "", Err: nil},
	)

	err := Cleanup(distinctRepoRoot, "/tmp/.golemic/proj", 1, mockExec)
	if err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}

	calls := mockExec.Calls()
	if len(calls) < 1 {
		t.Fatal("expected at least 1 executor call")
	}
	expectedPath := filepath.Join("/tmp/.golemic/proj", "worktrees", "issue-1")
	expectCall(t, calls[0], "", "git", "-C", distinctRepoRoot, "worktree", "remove", expectedPath)
	expectCall(t, calls[1], "", "git", "-C", distinctRepoRoot, "branch", "-D", "golemic/issue-1")
}

// TestCleanup_NonExistentWorktree — the executor's error is propagated;
// Cleanup does not silently ignore failures.
func TestCleanup_NonExistentWorktree(t *testing.T) {
	mockExec := newMockExecutor(
		execResponse{Stdout: "", Err: errors.New("fatal: 'issue-999' is not a working tree")},
	)
	_, golemicDir, _, issueNum, _ := testCreateArgs()

	err := Cleanup(defaultRepoRoot, golemicDir, issueNum, mockExec)
	if err == nil {
		t.Fatal("expected error for non-existent worktree, got nil")
	}
}