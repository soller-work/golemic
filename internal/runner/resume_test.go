package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golemic/internal/agent"
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupResumeRunner creates a minimal Runner ready for resumeOrchestrate unit tests.
func setupResumeRunner(t *testing.T, exec *fakeExecutor) (*Runner, string, *bytes.Buffer) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)

	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	shortHome := "/tmp"
	shortProject := "rs"
	shortRunID := "issue-42-resume"
	t.Cleanup(func() { os.RemoveAll(filepath.Join(shortHome, ".golemic", shortProject)) }) //nolint:errcheck

	configJSON := fmt.Sprintf(`{"project":%q,"verify_command":"go test","codebase_memory":{"enabled":false}}`, shortProject)
	if err := os.WriteFile(filepath.Join(repoRoot, ".golemic", "config.json"), []byte(configJSON), 0644); err != nil {
		t.Fatal(err)
	}
	credDir := filepath.Join(shortHome, ".golemic", shortProject)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := fmt.Sprintf(`{"dev_token":%q,"reviewer_token":%q}`, creds.DevToken(), creds.ReviewerToken())
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}

	golemicDir := filepath.Join(repoRoot, ".golemic")
	guidelinesDir := filepath.Join(golemicDir, "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "dev.md"), []byte("# Dev Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guidelinesDir, "reviewer.md"), []byte("# Reviewer Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	agentsDir := filepath.Join(golemicDir, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"dev", "reviewer"} {
		if err := os.WriteFile(filepath.Join(agentsDir, role+".md"), []byte("---\nmodel: test/model\n---\npersona body\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	r := New(exec, shortHome, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = shortProject
	r.homeDir = shortHome
	r.runID = shortRunID
	r.branchName = "golemic/issue-42"
	r.creds = creds
	r.cfg = &config.Config{
		VerifyCommand:   "go test",
		TimeoutMinutes:  30,
		MaxReviewRounds: 3,
	}
	r.issue = &issueData{Number: 42, Title: "Test Issue"}

	var stderr bytes.Buffer
	r.SetStderr(&stderr)

	injectFakeGMBrokerPP(t)

	return r, filepath.Join(shortHome, ".golemic", shortProject, "runs", shortRunID, "events.jsonl"), &stderr
}

// runResumeOrchestrate prepares the event log and runs resumeOrchestrate.
func runResumeOrchestrate(t *testing.T, r *Runner, logPath string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	w, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("create log writer: %v", err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"issue": 42, "runId": "issue-42-resume"})
	_ = w.Write(eventlog.Event{
		Type:    eventlog.EventRunStarted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   "issue-42-resume",
		Payload: payload,
	})
	w.Close() //nolint:errcheck

	writer, err := eventlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("open resume writer: %v", err)
	}
	defer writer.Close() //nolint:errcheck
	return r.resumeOrchestrate(writer, logPath, "")
}

// prListJSON returns a gh pr list --state all JSON response.
func prListJSON(number int, state, url string, labels ...string) string {
	type label struct {
		Name string `json:"name"`
	}
	lbls := make([]label, len(labels))
	for i, l := range labels {
		lbls[i] = label{Name: l}
	}
	prs := []map[string]interface{}{
		{"number": number, "url": url, "state": state, "labels": lbls},
	}
	b, _ := json.Marshal(prs)
	return string(b)
}

// reviewsJSON returns a gh api repos/.../reviews JSON response.
func reviewsJSON(reviews ...map[string]interface{}) string {
	if reviews == nil {
		return "[]"
	}
	b, _ := json.Marshal(reviews)
	return string(b)
}

// pendingReviewsGraphQLResponse returns a GraphQL response for pending reviews.
func pendingReviewsGraphQLResponse(nodes ...map[string]interface{}) string {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"repository": map[string]interface{}{
				"pullRequest": map[string]interface{}{
					"reviews": map[string]interface{}{
						"nodes": nodes,
					},
				},
			},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// botLoginJSON returns the gh api user response for a given login.
func botLoginJSON(login string) string {
	b, _ := json.Marshal(map[string]string{"login": login})
	return string(b)
}

// repoViewJSON returns the gh repo view --json owner,name response.
func repoViewJSON() string {
	return `{"owner":{"login":"testowner"},"name":"testrepo"}`
}

// baseResumeRunFunc returns a git runFunc that returns a non-empty result for ls-remote
// (so the remote branch check passes) and delegates everything else to handleGitCmd.
func baseResumeRunFunc() func(string, ...string) (string, error) {
	return func(name string, args ...string) (string, error) {
		if name != "git" {
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		}
		sub := ""
		if len(args) >= 3 && args[0] == "-C" {
			sub = args[2]
		} else if len(args) >= 1 {
			sub = args[0]
		}
		if sub == "ls-remote" {
			return "abc123\trefs/heads/golemic/issue-42\n", nil
		}
		return handleGitCmd(args)
	}
}

// baseResumeWithEnvFunc returns a gh runWithEnvFunc for happy-path resume tests.
func baseResumeWithEnvFunc(
	prNumber int, prState string, prLabels []string, botLogin string,
	reviews []map[string]interface{}, pendingNodes []map[string]interface{},
) func(map[string]string, string, ...string) (string, error) {
	const ciCheckRunsJSON = `{"check_runs":[{"name":"verify","status":"completed","conclusion":"success"}]}`
	nodes := pendingNodes
	if nodes == nil {
		nodes = []map[string]interface{}{}
	}
	return func(env map[string]string, name string, args ...string) (string, error) {
		if name == "git" && len(args) >= 1 && args[0] == "push" {
			return "", nil
		}
		if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			return "https://github.com/testowner/testrepo/pull/99\n", nil
		}
		if name != "gh" {
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		}
		return dispatchResumeGH(prNumber, prState, prLabels, botLogin, reviews, nodes, ciCheckRunsJSON, args)
	}
}

// dispatchResumeGH routes gh subcommand calls for resume tests.
func dispatchResumeGH(
	prNumber int, prState string, prLabels []string, botLogin string,
	reviews []map[string]interface{}, pendingNodes []map[string]interface{},
	ciCheckRunsJSON string, args []string,
) (string, error) {
	switch args[0] {
	case "issue":
		return `{"title":"Test Issue","labels":[],"state":"OPEN"}`, nil
	case "pr":
		return dispatchResumeGHPR(prNumber, prState, prLabels, args)
	case "repo":
		return dispatchResumeGHRepo(args)
	case "api":
		return dispatchResumeGHAPI(botLogin, reviews, pendingNodes, ciCheckRunsJSON, args)
	}
	return "", fmt.Errorf("not mocked: gh %v", args)
}

// dispatchResumeGHPR handles gh pr sub-commands in resume tests.
func dispatchResumeGHPR(prNumber int, prState string, prLabels []string, args []string) (string, error) {
	switch args[1] {
	case "list":
		return prListJSON(prNumber, prState, fmt.Sprintf("https://github.com/testowner/testrepo/pull/%d", prNumber), prLabels...), nil
	case "view":
		return ciTestHeadSHA + "\n", nil
	case "merge":
		return "merged-sha\n", nil
	case "comment", "edit":
		return "", nil
	}
	return "", fmt.Errorf("not mocked: gh pr %v", args)
}

// dispatchResumeGHRepo handles gh repo sub-commands in resume tests.
func dispatchResumeGHRepo(args []string) (string, error) {
	if strings.Contains(strings.Join(args, " "), "nameWithOwner") {
		return "testowner/testrepo\n", nil
	}
	return repoViewJSON(), nil
}

// dispatchResumeGHAPI handles gh api sub-commands in resume tests.
func dispatchResumeGHAPI(
	botLogin string, reviews []map[string]interface{}, pendingNodes []map[string]interface{},
	ciCheckRunsJSON string, args []string,
) (string, error) {
	switch args[1] {
	case "user":
		return botLoginJSON(botLogin), nil
	case "graphql":
		return dispatchResumeGHAPIGraphQL(pendingNodes, args)
	}
	if strings.HasPrefix(args[1], "repos/") {
		return dispatchResumeGHAPIRepos(reviews, ciCheckRunsJSON, args)
	}
	return "", fmt.Errorf("not mocked: gh api %v", args)
}

// dispatchResumeGHAPIGraphQL handles gh api graphql calls in resume tests.
func dispatchResumeGHAPIGraphQL(pendingNodes []map[string]interface{}, args []string) (string, error) {
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "PENDING") && strings.Contains(joined, "author") {
		return pendingReviewsGraphQLResponse(pendingNodes...), nil
	}
	return `{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[]}}}}}`, nil
}

// dispatchResumeGHAPIRepos handles gh api repos/... calls in resume tests.
func dispatchResumeGHAPIRepos(reviews []map[string]interface{}, ciCheckRunsJSON string, args []string) (string, error) {
	path := args[1]
	if strings.Contains(path, "/reviews") && !strings.Contains(path, "/comments") {
		return reviewsJSON(reviews...), nil
	}
	if strings.Contains(path, "/comments") {
		return "[]", nil
	}
	if strings.Contains(path, "check-runs") {
		return ciCheckRunsJSON, nil
	}
	return "[]", nil
}

// baseResumeExecutor builds a fakeExecutor for happy-path resume tests.
func baseResumeExecutor(
	prNumber int, prState string, prLabels []string, botLogin string,
	reviews []map[string]interface{}, pendingNodes []map[string]interface{},
) *fakeExecutor {
	return &fakeExecutor{
		runFunc:        baseResumeRunFunc(),
		runWithEnvFunc: baseResumeWithEnvFunc(prNumber, prState, prLabels, botLogin, reviews, pendingNodes),
	}
}

// makeResumeFakeAgent returns a runAgentFn for resume tests.
// Unlike makeOrchestrateFakeAgent, it does NOT write pr_opened for the first dev call
// (since in resume mode, pr_opened is already synthesized by resumeOrchestrate).
func makeResumeFakeAgent(t *testing.T, rounds []agentRoundConfig, capture *promptCapture) func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	callIdx := 0
	return func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		if callIdx >= len(rounds) {
			t.Errorf("unexpected agent call #%d (only %d configured)", callIdx+1, len(rounds))
			return 1, agent.TranscriptPaths{}, fmt.Errorf("unexpected call")
		}
		round := rounds[callIdx]
		callIdx++

		if round.role != "" && cfg.Role != round.role {
			t.Errorf("agent call #%d: expected role %q, got %q", callIdx, round.role, cfg.Role)
		}
		if cfg.Role == "dev" && capture != nil {
			capture.devPrompts = append(capture.devPrompts, cfg.UserPrompt)
		}
		if round.doTimeout {
			return 0, agent.TranscriptPaths{}, agent.ErrTimeout
		}
		if round.doStalled {
			return 0, agent.TranscriptPaths{}, agent.ErrStalled
		}
		if cfg.Role == "dev" {
			if !sendGMProjectCheck(cfg.Env) {
				t.Fatalf("makeResumeFakeAgent: sendGMProjectCheck failed")
			}
			if !sendGMDevDone(cfg.Env) {
				t.Fatalf("makeResumeFakeAgent: sendGMDevDone failed")
			}
		}
		if cfg.Role == "reviewer" && round.verdict != "" {
			writeReviewEvent(t, cfg.EventLogPath, round.verdict, round.body)
		}
		return round.exitCode, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
	}
}

// ---------------------------------------------------------------------------
// AC: Normal run without --resume still collides
// ---------------------------------------------------------------------------

func TestResume_NormalRun_CollisionUnchanged(t *testing.T) {
	homeDir, repoRoot, project := setupRunnerTest(t)

	// Pre-create worktree directory to simulate collision
	worktreeDir := filepath.Join(homeDir, ".golemic", project, "worktrees", "issue-42")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}

	exec := setupHappyExecutor(repoRoot)
	var stdout, stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStdout(&stdout)
	r.SetStderr(&stderr)
	// resume is NOT set

	exitCode := r.Run()

	if exitCode != 1 {
		t.Fatalf("exit code: got %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "Worktree exists at") {
		t.Errorf("expected collision message, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC: No PR found → resume blocks
// ---------------------------------------------------------------------------

func TestResume_NoPR_Blocks(t *testing.T) {
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			if name == "git" {
				return handleGitCmd(args)
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "list" {
				return "[]", nil // no PRs
			}
			return "", fmt.Errorf("not mocked: gh %v", args)
		},
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeAborted)
	}
	if !strings.Contains(stderr.String(), "resume requires an existing PR") {
		t.Errorf("expected 'resume requires an existing PR' in stderr, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC: Multiple PRs found → resume blocks
// ---------------------------------------------------------------------------

func TestResume_MultiplePRs_Blocks(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "list" {
				return `[{"number":1,"url":"https://github.com/o/r/pull/1","state":"OPEN","labels":[]},{"number":2,"url":"https://github.com/o/r/pull/2","state":"OPEN","labels":[]}]`, nil
			}
			return "", fmt.Errorf("not mocked: gh %v", args)
		},
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeAborted)
	}
	if !strings.Contains(stderr.String(), "multiple PRs found") {
		t.Errorf("expected 'multiple PRs found' in stderr, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC: Malformed PR list JSON → resume blocks
// ---------------------------------------------------------------------------

func TestResume_MalformedPRList_Blocks(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "list" {
				return "{not json", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeAborted)
	}
	if !strings.Contains(stderr.String(), "parse PR list") {
		t.Errorf("stderr should mention PR list parsing, got: %q", stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Errorf("no agent calls expected on malformed PR list, got %d", len(agentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC: Malformed reviews JSON → resume blocks
// ---------------------------------------------------------------------------

func TestResume_MalformedReviewsJSON_Blocks(t *testing.T) {
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", nil, nil)

	inner := exec.runWithEnvFunc
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && args[0] == "api" && strings.Contains(args[1], "/reviews") {
			return "{not json", nil
		}
		return inner(env, name, args...)
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeAborted)
	}
	if !strings.Contains(stderr.String(), "parse reviews") {
		t.Errorf("stderr should mention review parsing, got: %q", stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Errorf("no agent calls expected on malformed reviews JSON, got %d", len(agentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC: PR closed but not merged → resume blocks
// ---------------------------------------------------------------------------

func TestResume_PRClosedNotMerged_Blocks(t *testing.T) {
	exec := baseResumeExecutor(7, "CLOSED", nil, "golemic-reviewer", nil, nil)
	r, logPath, stderr := setupResumeRunner(t, exec)
	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeAborted)
	}
	if !strings.Contains(stderr.String(), "closed but not merged") {
		t.Errorf("expected 'closed but not merged' in stderr, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC: PR already merged → idempotent success
// ---------------------------------------------------------------------------

func TestResume_PRAlreadyMerged_IdempotentSuccess(t *testing.T) {
	exec := baseResumeExecutor(7, "MERGED", nil, "golemic-reviewer", nil, nil)
	r, logPath, stderr := setupResumeRunner(t, exec)
	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already merged") {
		t.Errorf("expected 'already merged' in stderr, got: %q", stderr.String())
	}

	// Verify pr_opened event was synthesized
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	hasPROpened := false
	for _, ev := range events {
		if ev.Type == eventlog.EventPROpened {
			hasPROpened = true
		}
	}
	if !hasPROpened {
		t.Error("expected pr_opened event to be synthesized for merged PR")
	}
}

// ---------------------------------------------------------------------------
// AC: Remote branch missing → resume blocks
// ---------------------------------------------------------------------------

func missingRemoteRunFunc() func(string, ...string) (string, error) {
	return func(name string, args ...string) (string, error) {
		if name != "git" {
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		}
		sub := ""
		if len(args) >= 3 && args[0] == "-C" {
			sub = args[2]
		} else if len(args) >= 1 {
			sub = args[0]
		}
		if sub == "ls-remote" {
			return "", nil // empty = remote branch missing
		}
		return handleGitCmd(args)
	}
}

func TestResume_RemoteBranchMissing_Blocks(t *testing.T) {
	exec := &fakeExecutor{
		runFunc: missingRemoteRunFunc(),
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "pr" && args[1] == "list" {
				return prListJSON(7, "OPEN", "https://github.com/o/r/pull/7"), nil
			}
			if name == "gh" && args[0] == "api" && args[1] == "user" {
				return botLoginJSON("golemic-reviewer"), nil
			}
			return "", fmt.Errorf("not mocked: gh %v", args)
		},
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeAborted, stderr.String())
	}
	if !strings.Contains(stderr.String(), "remote branch") {
		t.Errorf("expected 'remote branch' in stderr, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC: Open PR, no submitted reviews → CI gate + reviewer round
// ---------------------------------------------------------------------------

func TestResume_NoReviews_StartsCI_ThenReviewer(t *testing.T) {
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", nil, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		if cfg.Role == "reviewer" {
			writeReviewEvent(t, cfg.EventLogPath, "approved", "LGTM")
		}
		return 0, agent.TranscriptPaths{Stderr: "/tmp/stderr"}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	// Should have called reviewer (not dev, since no initial dev turn in resume)
	if len(agentCalls) != 1 || agentCalls[0] != "reviewer" {
		t.Errorf("expected [reviewer], got %v", agentCalls)
	}

	// pr_opened event must be in the log
	reader := eventlog.Reader{}
	events, _ := reader.Read(logPath)
	hasPROpened := false
	for _, ev := range events {
		if ev.Type == eventlog.EventPROpened {
			hasPROpened = true
		}
	}
	if !hasPROpened {
		t.Error("pr_opened event missing from event log")
	}
}

// ---------------------------------------------------------------------------
// AC: Last review CHANGES_REQUESTED → dev retry with findings
// ---------------------------------------------------------------------------

func TestResume_ChangesRequested_DevRetry_ThenReviewer(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 101, "state": "CHANGES_REQUESTED", "body": "Fix the typo in main.go", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)
	capture := &promptCapture{}
	r.SetRunAgentFn(makeResumeFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "Good now", exitCode: 0},
	}, capture))

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if len(capture.devPrompts) != 1 {
		t.Fatalf("expected 1 dev call, got %d", len(capture.devPrompts))
	}
	if !strings.Contains(capture.devPrompts[0], "Fix the typo in main.go") {
		t.Errorf("dev retry prompt must contain review findings, got: %s", capture.devPrompts[0])
	}
}

// ---------------------------------------------------------------------------
// AC: Last review APPROVED + no confidence:low label → merge path
// ---------------------------------------------------------------------------

func TestResume_Approved_ConfidenceHigh_MergePath(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 200, "state": "APPROVED", "body": "LGTM", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", []string{"confidence:high"}, "golemic-reviewer", reviews, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Errorf("expected 0 agent calls, got %d: %v", len(agentCalls), agentCalls)
	}

	reader := eventlog.Reader{}
	events, _ := reader.Read(logPath)
	hasApprovedReview := false
	for _, ev := range events {
		if ev.Type == eventlog.EventReviewSubmitted {
			var d struct {
				Verdict         string `json:"verdict"`
				MergeConfidence string `json:"mergeConfidence"`
			}
			_ = json.Unmarshal(ev.Payload, &d)
			if d.Verdict == "approved" && d.MergeConfidence == "high" {
				hasApprovedReview = true
			}
		}
	}
	if !hasApprovedReview {
		t.Error("expected synthesized review_submitted event with verdict=approved and mergeConfidence=high")
	}
}

// AC: Last review APPROVED with no clear confidence source → skip auto-merge.
func TestResume_Approved_NoConfidenceSource_SkipsMerge(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 201, "state": "APPROVED", "body": "LGTM", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Errorf("expected 0 agent calls for skipped automerge, got %d: %v", len(agentCalls), agentCalls)
	}

	reader := eventlog.Reader{}
	events, _ := reader.Read(logPath)
	hasSkipped := false
	for _, ev := range events {
		if ev.Type == eventlog.EventAutomergeSkipped {
			hasSkipped = true
		}
	}
	if !hasSkipped {
		t.Error("expected automerge_skipped event when confidence is missing")
	}
}

// ---------------------------------------------------------------------------
// AC: Last review APPROVED + confidence:low label → skip auto-merge (success, not merge)
// ---------------------------------------------------------------------------

func TestResume_Approved_ConfidenceLow_SkipsMerge(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 201, "state": "APPROVED", "body": "Approved but uncertain", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", []string{"confidence:low"}, "golemic-reviewer", reviews, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	// automerge_skipped is treated as success (BR-008)
	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Errorf("expected 0 agent calls for skipped automerge, got %d: %v", len(agentCalls), agentCalls)
	}

	reader := eventlog.Reader{}
	events, _ := reader.Read(logPath)
	hasSkipped := false
	for _, ev := range events {
		if ev.Type == eventlog.EventAutomergeSkipped {
			hasSkipped = true
		}
	}
	if !hasSkipped {
		t.Error("expected automerge_skipped event")
	}
}

// ---------------------------------------------------------------------------
// AC: 3 bot CHANGES_REQUESTED reviews → escalate without more agent turns
// ---------------------------------------------------------------------------

func TestResume_ThreeBotCRs_Escalates(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 301, "state": "CHANGES_REQUESTED", "body": "Fix A", "user": map[string]interface{}{"login": "golemic-reviewer"}},
		{"id": 302, "state": "CHANGES_REQUESTED", "body": "Fix B", "user": map[string]interface{}{"login": "golemic-reviewer"}},
		{"id": 303, "state": "CHANGES_REQUESTED", "body": "Fix C", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)
	r.cfg.MaxReviewRounds = 3

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeEscalated {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeEscalated, stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Errorf("expected 0 agent calls after escalation, got %d: %v", len(agentCalls), agentCalls)
	}
}

// ---------------------------------------------------------------------------
// AC: 2 bot CRs + 1 human CR → human doesn't count against bot limit; 1 more bot cycle allowed
// ---------------------------------------------------------------------------

func TestResume_MixedReviews_HumanCRDoesNotCountAgainstBotLimit(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 401, "state": "CHANGES_REQUESTED", "body": "Bot fix A", "user": map[string]interface{}{"login": "golemic-reviewer"}},
		{"id": 402, "state": "CHANGES_REQUESTED", "body": "Bot fix B", "user": map[string]interface{}{"login": "golemic-reviewer"}},
		{"id": 403, "state": "CHANGES_REQUESTED", "body": "Human says: fix X too", "user": map[string]interface{}{"login": "human-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)
	r.cfg.MaxReviewRounds = 3

	capture := &promptCapture{}
	r.SetRunAgentFn(makeResumeFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0}, // dev retry from human CR
		{role: "reviewer", verdict: "approved", body: "Now good", exitCode: 0}, // 3rd bot round → approved
	}, capture))

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	// Dev retry should use the human CR's findings
	if len(capture.devPrompts) != 1 {
		t.Fatalf("expected 1 dev call, got %d", len(capture.devPrompts))
	}
	if !strings.Contains(capture.devPrompts[0], "Human says: fix X too") {
		t.Errorf("dev retry must use human CR findings, got: %s", capture.devPrompts[0])
	}
}

// ---------------------------------------------------------------------------
// AC: 2 bot CRs + 1 human CR; next reviewer also requests changes → escalates at round 3
// ---------------------------------------------------------------------------

func TestResume_MixedReviews_ThirdBotCREscalates(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 401, "state": "CHANGES_REQUESTED", "body": "Bot fix A", "user": map[string]interface{}{"login": "golemic-reviewer"}},
		{"id": 402, "state": "CHANGES_REQUESTED", "body": "Bot fix B", "user": map[string]interface{}{"login": "golemic-reviewer"}},
		{"id": 403, "state": "CHANGES_REQUESTED", "body": "Human request", "user": map[string]interface{}{"login": "human-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	r, logPath, _ := setupResumeRunner(t, exec)
	r.cfg.MaxReviewRounds = 3

	r.SetRunAgentFn(makeResumeFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "changes_requested", body: "Still needs work", exitCode: 0},
	}, nil))

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeEscalated {
		t.Errorf("outcome: got %q, want %q", outcome, outcomeEscalated)
	}
}

// ---------------------------------------------------------------------------
// AC: Bot pending review → deleted before reviewer round after dedicated proof
// ---------------------------------------------------------------------------

func TestResume_BotPendingReview_DeletedBeforeReviewer(t *testing.T) {
	// pendingNodes: one pending review from the bot itself
	pendingNodes := []map[string]interface{}{
		{"id": "PRR_bot123", "author": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	// baseResumeExecutor's graphql handler uses the pendingNodes for sweepPendingReviews.
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", nil, pendingNodes)

	// Override to also handle the sweepPendingReviews delete mutation.
	// Must distinguish between the delete mutation and the pending-review discovery call.
	deleteCalled := false
	inner := exec.runWithEnvFunc
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && args[0] == "api" && args[1] == "graphql" {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "deletePullRequestReview") {
				deleteCalled = true
				return `{"data":{"deletePullRequestReview":{"pullRequestReview":{"id":"PRR_bot123"}}}}`, nil
			}
			// sweepPendingReviews discover call (graphqlDiscoverPending has no "author" field).
			if strings.Contains(joined, "states:[PENDING]") && !strings.Contains(joined, "author") {
				return `{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[{"id":"PRR_bot123"}]}}}}}`, nil
			}
		}
		return inner(env, name, args...)
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	r.SetRunAgentFn(makeResumeFakeAgent(t, []agentRoundConfig{
		{role: "reviewer", verdict: "approved", body: "LGTM", exitCode: 0},
	}, nil))

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if !deleteCalled {
		t.Error("expected bot pending review to be deleted via sweepPendingReviews")
	}
}

// ---------------------------------------------------------------------------
// AC: Human pending review proof blocks resume before a reviewer turn.
// ---------------------------------------------------------------------------
func TestResume_HumanPendingReview_Blocks(t *testing.T) {
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", nil, []map[string]interface{}{
		{"id": "PRR_human456", "author": map[string]interface{}{"login": "alice"}},
	})

	deleteCalled := false
	inner := exec.runWithEnvFunc
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && args[0] == "api" && args[1] == "graphql" && strings.Contains(strings.Join(args, " "), "deletePullRequestReview") {
			deleteCalled = true
			return "", fmt.Errorf("should not be called")
		}
		return inner(env, name, args...)
	}

	r, logPath, stderr := setupResumeRunner(t, exec)

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeAborted, stderr.String())
	}
	if !strings.Contains(stderr.String(), "alice") {
		t.Errorf("stderr should mention the human reviewer login, got: %q", stderr.String())
	}
	if deleteCalled {
		t.Error("must not delete human's pending review")
	}
	if len(agentCalls) != 0 {
		t.Errorf("no agent calls expected when blocked by human pending review, got %d", len(agentCalls))
	}
}

// AC: Pending review proof cannot be obtained → resume blocks before reviewer turn.
func TestResume_PendingProofUnavailable_Blocks(t *testing.T) {
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", nil, nil)

	inner := exec.runWithEnvFunc
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && args[0] == "api" && args[1] == "graphql" && strings.Contains(strings.Join(args, " "), "states:[PENDING]") {
			return "{not json", nil
		}
		return inner(env, name, args...)
	}

	r, logPath, stderr := setupResumeRunner(t, exec)

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeAborted, stderr.String())
	}
	if !strings.Contains(stderr.String(), "pending review proof") {
		t.Errorf("stderr should mention pending review proof failure, got: %q", stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Errorf("no agent calls expected when pending proof is unavailable, got %d", len(agentCalls))
	}
}

// ---------------------------------------------------------------------------
// AC: CHANGES_REQUESTED with empty body and no inline comments → blocks
// ---------------------------------------------------------------------------

func TestResume_ChangesRequested_EmptyFindings_Blocks(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 501, "state": "CHANGES_REQUESTED", "body": "", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	r, logPath, stderr := setupResumeRunner(t, exec)

	var agentCalls []string
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		agentCalls = append(agentCalls, cfg.Role)
		return 0, agent.TranscriptPaths{}, nil
	})

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeAborted {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeAborted, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no findings") {
		t.Errorf("expected 'no findings' in stderr, got: %q", stderr.String())
	}
	if len(agentCalls) != 0 {
		t.Error("no agent calls expected when blocked by empty findings")
	}
}

// ---------------------------------------------------------------------------
// AC: pr_opened event is synthesized in resume log for normal open PR case
// ---------------------------------------------------------------------------

func TestResume_SynthesizesPROpenedEvent(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 600, "state": "APPROVED", "body": "OK", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}
	exec := baseResumeExecutor(99, "OPEN", nil, "golemic-reviewer", reviews, nil)

	r, logPath, _ := setupResumeRunner(t, exec)
	r.SetRunAgentFn(func(ctx context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		return 0, agent.TranscriptPaths{}, nil
	})

	_ = runResumeOrchestrate(t, r, logPath)

	// Verify pr_opened event exists and contains correct PR number
	reader := eventlog.Reader{}
	events, _ := reader.Read(logPath)
	var prNum string
	for _, ev := range events {
		if ev.Type == eventlog.EventPROpened {
			var p map[string]string
			_ = json.Unmarshal(ev.Payload, &p)
			prNum = p["prNumber"]
		}
	}
	if prNum != "99" {
		t.Errorf("pr_opened event: prNumber = %q, want %q", prNum, "99")
	}
}

// ---------------------------------------------------------------------------
// AC: --resume flag is parsed by CLI and passed to runner (integration smoke test)
// ---------------------------------------------------------------------------

func TestResume_CLIFlagParsed(t *testing.T) {
	homeDir, repoRoot, _ := setupRunnerTest(t)

	// Use setupHappyExecutor as the base (handles git + basic gh calls) and
	// override gh pr list to return no PRs so resume fails cleanly.
	base := setupHappyExecutor(repoRoot)
	exec := &fakeExecutor{
		runFunc: base.runFunc,
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && args[0] == "issue" {
				return `{"title":"T","labels":[],"state":"OPEN"}`, nil
			}
			if name == "gh" && args[0] == "pr" && args[1] == "list" {
				// No PR found: resume should fail-closed with a clear message
				return "[]", nil
			}
			return base.runWithEnvFunc(env, name, args...)
		},
	}

	var stderr bytes.Buffer
	r := New(exec, homeDir, repoRoot, 42)
	r.SetPreflighter(passingPreflighter{})
	r.SetStderr(&stderr)
	r.SetResume(true)

	exitCode := r.Run()

	// Should fail because no PR is found, but NOT because of a collision
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if strings.Contains(stderr.String(), "Worktree exists at") || strings.Contains(stderr.String(), "Branch golemic") {
		t.Errorf("should not report a collision when --resume is set, got: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "resume") {
		t.Errorf("expected resume-related error message, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// AC: CHANGES_REQUESTED with inline comments but empty body → uses inline comments
// ---------------------------------------------------------------------------

func TestResume_ChangesRequested_InlineCommentsAsFindings(t *testing.T) {
	reviews := []map[string]interface{}{
		{"id": 700, "state": "CHANGES_REQUESTED", "body": "", "user": map[string]interface{}{"login": "golemic-reviewer"}},
	}

	exec := baseResumeExecutor(7, "OPEN", nil, "golemic-reviewer", reviews, nil)

	// Override inline comments endpoint to return a comment
	inner := exec.runWithEnvFunc
	exec.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && args[0] == "api" && strings.HasSuffix(args[1], "/comments") {
			return `[{"path":"main.go","line":10,"side":"RIGHT","body":"fix this"}]`, nil
		}
		return inner(env, name, args...)
	}

	r, logPath, stderr := setupResumeRunner(t, exec)
	capture := &promptCapture{}
	r.SetRunAgentFn(makeResumeFakeAgent(t, []agentRoundConfig{
		{role: "dev", exitCode: 0},
		{role: "reviewer", verdict: "approved", body: "Fixed", exitCode: 0},
	}, capture))

	outcome := runResumeOrchestrate(t, r, logPath)

	if outcome != outcomeSuccess {
		t.Errorf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	// Dev retry should have been invoked (inline comments are the findings)
	if len(capture.devPrompts) != 1 {
		t.Fatalf("expected 1 dev call, got %d", len(capture.devPrompts))
	}
}
