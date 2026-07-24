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

	"golemic/internal/agent"
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/gmbroker"
	"golemic/internal/prompt"
)

type reviewerGraphQLState struct {
	pendingReviewID string
	createCalls     int
	deleteCalls     int
	submitCalls     int
	submitReviewID  string
}

func newReviewerGraphQLExecutor(commentJSON string) (*fakeExecutor, *reviewerGraphQLState) { //nolint:cyclop
	state := &reviewerGraphQLState{}
	base := pingPongExecutor(false, nil)
	inner := base.runWithEnvFunc
	base.runWithEnvFunc = func(env map[string]string, name string, args ...string) (string, error) {
		if name == "gh" && len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			return `{"owner":{"login":"testowner"},"name":"testrepo"}`, nil
		}
		if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "deletePullRequestReview"):
				state.deleteCalls++
				return `{"data":{"deletePullRequestReview":{"pullRequestReview":{"id":"PRR_deleted"}}}}`, nil
			case strings.Contains(joined, "viewer{login}") && strings.Contains(joined, "reviews(first:10,states:[PENDING])"):
				if state.pendingReviewID == "" {
					return `{"data":{"viewer":{"login":"golemic-reviewer"},"repository":{"pullRequest":{"id":"PR_42","reviews":{"nodes":[]}}}}}`, nil
				}
				return fmt.Sprintf(`{"data":{"viewer":{"login":"golemic-reviewer"},"repository":{"pullRequest":{"id":"PR_42","reviews":{"nodes":[{"id":%q,"author":{"login":"golemic-reviewer"}}]}}}}}`, state.pendingReviewID), nil
			case strings.Contains(joined, "reviews(first:1,states:[PENDING])"):
				return `{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[]}}}}}`, nil
			case strings.Contains(joined, "addPullRequestReview(input"):
				state.createCalls++
				state.pendingReviewID = "PRR_1"
				return `{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"PRR_1"}}}}`, nil
			case strings.Contains(joined, "addPullRequestReviewThread"):
				return `{"data":{"addPullRequestReviewThread":{"thread":{"id":"T1"}}}}`, nil
			case strings.Contains(joined, "submitPullRequestReview"):
				state.submitCalls++
				for _, a := range args {
					if strings.HasPrefix(a, "reviewId=") {
						state.submitReviewID = strings.TrimPrefix(a, "reviewId=")
						break
					}
				}
				return `{"data":{"submitPullRequestReview":{"pullRequestReview":{"fullDatabaseId":"9001","comments":{"totalCount":1}}}}}`, nil
			}
		}
		if name == "gh" && len(args) >= 2 && args[0] == "api" && strings.HasPrefix(args[1], "repos/") && strings.Contains(args[1], "/reviews/") && strings.HasSuffix(args[1], "/comments") {
			return commentJSON, nil
		}
		return inner(env, name, args...)
	}
	return base, state
}

func newReviewerSubmitRunner(t *testing.T, exec *fakeExecutor) (*Runner, string, *bytes.Buffer) {
	t.Helper()
	homeDir, repoRoot, project := setupRunnerTest(t)
	loader := credentials.NewLoader(homeDir)
	creds, err := loader.Load(project)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	r := New(exec, homeDir, repoRoot, 42)
	r.repoRoot = repoRoot
	r.project = project
	r.homeDir = homeDir
	r.runID = "review-test-run"
	r.branchName = "golemic/issue-42"
	r.creds = creds
	r.issue = &issueData{Number: 42, Title: "Test Issue"}
	r.cfg = &config.Config{VerifyCommand: "go test", MaxReviewRounds: 5}

	logPath := filepath.Join(homeDir, ".golemic", project, "runs", r.runID, "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatal(err)
	}
	return r, logPath, &bytes.Buffer{}
}

// writePROpenedEvent is provided by pingpong_test.go.
func writeReviewerPrecheck(t *testing.T, r *Runner, logPath string, ok bool, beforeFP, afterFP string) string {
	t.Helper()
	res := &reviewerPrecheckResult{
		OK:      ok,
		Command: "go test",
		ExitCode: func() int {
			if ok {
				return 0
			}
			return 1
		}(),
		BeforeFingerprint: beforeFP,
		AfterFingerprint:  afterFP,
	}
	writeReviewerPrecheckEvent(r, logPath, res)
	return buildReviewerPrecheckBlock(res)
}

func assertLatestReviewSubmittedEvent(t *testing.T, logPath, wantReviewID string, wantInlineCount int) {
	t.Helper()
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != eventlog.EventReviewSubmitted {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(events[i].Payload, &payload); err != nil {
			t.Fatalf("unmarshal review_submitted payload: %v", err)
		}
		if payload["reviewId"] != wantReviewID {
			t.Fatalf("reviewId: got %v, want %q", payload["reviewId"], wantReviewID)
		}
		if payload["inlineCommentCount"] != float64(wantInlineCount) {
			t.Fatalf("inlineCommentCount: got %v, want %d", payload["inlineCommentCount"], wantInlineCount)
		}
		return
	}
	t.Fatal("review_submitted event not found")
}

func submitReviewerAttempt(t *testing.T, cfg agent.RoleConfig, attempt int) {
	t.Helper()
	verdict := "approved"
	mergeConfidence := "high"
	body := "Looks good now"
	if attempt == 1 {
		verdict = "changes_requested"
		mergeConfidence = "low"
		body = "Please fix"
	}
	result := callGMTool(gmSockFromEnv(cfg.Env), "gm_review_submit", fmt.Sprintf("c%d", attempt), map[string]any{
		"verdict":         verdict,
		"mergeConfidence": mergeConfidence,
		"body":            body,
	})
	if result == nil || result["ok"] != true {
		t.Fatalf("expected accepted %s submit, got %v", verdict, result)
	}
}

func makeReviewerChangesRequestedAgent(t *testing.T, reviewerPrompts, devPrompts *[]string) func(context.Context, agent.RoleConfig) (int, agent.TranscriptPaths, error) {
	t.Helper()
	reviewerCalls := 0
	return func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			if !sendGMProjectCheck(cfg.Env) || !sendGMDevDone(cfg.Env) {
				t.Fatal("dev gate failed")
			}
			*devPrompts = append(*devPrompts, cfg.UserPrompt)
		case "reviewer":
			*reviewerPrompts = append(*reviewerPrompts, cfg.UserPrompt)
			reviewerCalls++
			submitReviewerAttempt(t, cfg, reviewerCalls)
		default:
			t.Fatalf("unexpected role %q", cfg.Role)
		}
		return 0, fakeTranscriptPaths("/tmp", cfg.Role), nil
	}
}

func makeReviewerInvalidApprovalAgent(t *testing.T) (func(context.Context, agent.RoleConfig) (int, agent.TranscriptPaths, error), *int) {
	t.Helper()
	reviewerCalls := 0
	return func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			if !sendGMProjectCheck(cfg.Env) || !sendGMDevDone(cfg.Env) {
				t.Fatal("dev gate failed")
			}
		case "reviewer":
			reviewerCalls++
			result := callGMTool(gmSockFromEnv(cfg.Env), "gm_review_submit", fmt.Sprintf("c%d", reviewerCalls), map[string]any{
				"verdict":         "approved",
				"mergeConfidence": "high",
				"body":            "Still approved",
			})
			if result == nil || result["ok"] != false || result["code"] != "REVIEWER_GATE" {
				t.Fatalf("expected gate rejection, got %v", result)
			}
		default:
			t.Fatalf("unexpected role %q", cfg.Role)
		}
		return 0, fakeTranscriptPaths("/tmp", cfg.Role), nil
	}, &reviewerCalls
}

func TestSubmitPendingReview_StringFullDatabaseID(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql" && strings.Contains(strings.Join(args, " "), "submitPullRequestReview") {
				return `{"data":{"submitPullRequestReview":{"pullRequestReview":{"fullDatabaseId":"9001","comments":{"totalCount":1}}}}}`, nil
			}
			return "", fmt.Errorf("unexpected call: %s %v", name, args)
		},
	}
	r, _, _ := newReviewerSubmitRunner(t, exec)

	submittedID, inlineCount, err := r.submitPendingReview("PRR_1", "approved", "LGTM")
	if err != nil {
		t.Fatalf("submitPendingReview: %v", err)
	}
	if submittedID != "9001" {
		t.Errorf("submitPendingReview id: got %q, want 9001", submittedID)
	}
	if inlineCount != 1 {
		t.Errorf("submitPendingReview inlineCount: got %d, want 1", inlineCount)
	}
}

func TestSubmitReviewAndWriteEvent_ApprovedWritesReviewSubmitted(t *testing.T) {
	exec, state := newReviewerGraphQLExecutor(`[]`)
	r, logPath, _ := newReviewerSubmitRunner(t, exec)
	writePROpenedEvent(t, logPath, 99)

	state.pendingReviewID = "PRR_approved"
	if err := r.submitReviewAndWriteEvent(&reviewerInvocationState{
		reviewSubmitParams: &gmbroker.ReviewSubmitParams{Verdict: "approved", MergeConfidence: "high", Body: "LGTM"},
		pendingReviewID:    state.pendingReviewID,
	}, logPath); err != nil {
		t.Fatalf("submitReviewAndWriteEvent: %v", err)
	}

	if state.submitCalls != 1 {
		t.Fatalf("expected 1 submit call, got %d", state.submitCalls)
	}
	if state.submitReviewID != state.pendingReviewID {
		t.Errorf("submit reviewId: got %q, want %q", state.submitReviewID, state.pendingReviewID)
	}

	verdict, err := r.latestReviewVerdict(logPath)
	if err != nil {
		t.Fatalf("latestReviewVerdict: %v", err)
	}
	if verdict != "approved" {
		t.Errorf("latestReviewVerdict: got %q, want approved", verdict)
	}

	reviewID, err := r.latestReviewID(logPath)
	if err != nil {
		t.Fatalf("latestReviewID: %v", err)
	}
	if reviewID != "9001" {
		t.Errorf("latestReviewID: got %q, want 9001", reviewID)
	}

	events, err := eventlog.Reader{}.Read(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	var reviewEvent *eventlog.Event
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			reviewEvent = &events[i]
			break
		}
	}
	if reviewEvent == nil {
		t.Fatal("expected review_submitted event")
	}
	var payload struct {
		Verdict            string `json:"verdict"`
		Body               string `json:"body"`
		PRNumber           int    `json:"prNumber"`
		MergeConfidence    string `json:"mergeConfidence"`
		ReviewID           string `json:"reviewId"`
		InlineCommentCount int    `json:"inlineCommentCount"`
	}
	if err := json.Unmarshal(reviewEvent.Payload, &payload); err != nil {
		t.Fatalf("unmarshal review payload: %v", err)
	}
	if payload.Verdict != "approved" || payload.Body != "LGTM" || payload.PRNumber != 99 || payload.MergeConfidence != "high" || payload.ReviewID != "9001" || payload.InlineCommentCount != 1 {
		t.Fatalf("unexpected review_submitted payload: %+v", payload)
	}
	const wantReviewPayload = `{"body":"LGTM","inlineCommentCount":1,"mergeConfidence":"high","prNumber":99,"reviewId":"9001","verdict":"approved"}`
	if string(reviewEvent.Payload) != wantReviewPayload {
		t.Fatalf("review_submitted payload mismatch:\n got: %s\nwant: %s", string(reviewEvent.Payload), wantReviewPayload)
	}
}

func TestSubmitReviewAndWriteEvent_ChangesRequestedBuildsFindingsJSON(t *testing.T) {
	exec, state := newReviewerGraphQLExecutor(`[{"path":"main.go","line":10,"side":"RIGHT","body":"needs work"}]`)
	r, logPath, _ := newReviewerSubmitRunner(t, exec)
	writePROpenedEvent(t, logPath, 99)

	state.pendingReviewID = "PRR_changes"
	if err := r.submitReviewAndWriteEvent(&reviewerInvocationState{
		reviewSubmitParams: &gmbroker.ReviewSubmitParams{Verdict: "changes_requested", MergeConfidence: "low", Body: "Please fix"},
		pendingReviewID:    state.pendingReviewID,
	}, logPath); err != nil {
		t.Fatalf("submitReviewAndWriteEvent: %v", err)
	}

	reviewID, err := r.latestReviewID(logPath)
	if err != nil {
		t.Fatalf("latestReviewID: %v", err)
	}
	if reviewID != "9001" {
		t.Errorf("latestReviewID: got %q, want 9001", reviewID)
	}

	findingsJSON, err := r.buildFindingsJSON(logPath)
	if err != nil {
		t.Fatalf("buildFindingsJSON: %v", err)
	}
	if findingsJSON == "" {
		t.Fatal("expected findings JSON for submitted review with inline comments")
	}
	if !strings.Contains(findingsJSON, "main.go") || !strings.Contains(findingsJSON, "needs work") {
		t.Errorf("findings JSON missing inline comment data: %s", findingsJSON)
	}
}

func TestRenderReviewerGateRetry_IncludesApprovalRule(t *testing.T) {
	_, repoRoot, _ := setupRunnerTest(t)
	guidelinesPath := filepath.Join(repoRoot, ".golemic", "guidelines", "reviewer.md")
	if err := os.MkdirAll(filepath.Dir(guidelinesPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guidelinesPath, []byte("# Guidelines"), 0644); err != nil {
		t.Fatal(err)
	}

	p, err := prompt.RenderReviewerGateRetry("precheck was not ok", 42, prompt.Issue{Number: 181, Title: "Reviewer gate"}, "go test", guidelinesPath, "## Precheck Result\n\nok: false\n", false)
	if err != nil {
		t.Fatalf("RenderReviewerGateRetry: %v", err)
	}
	for _, want := range []string{"Previous approval rejected", "gm_review_submit", "changes_requested", "approved"} {
		if !strings.Contains(p, want) {
			t.Errorf("gate retry prompt missing %q", want)
		}
	}
	for _, banned := range []string{"golemic review-comment", "golemic submit-review", "git diff origin/main...HEAD"} {
		if strings.Contains(p, banned) {
			t.Errorf("gate retry prompt must not contain %q", banned)
		}
	}
}

func TestOrchestrate_ReviewerInvalidApproval_RestartsWithGatePromptAndPreservesPendingReview(t *testing.T) { //nolint:funlen,gocognit,cyclop
	exec, state := newReviewerGraphQLExecutor(`[]`)
	r, logPath, stderr := setupPingPongRunner(t, exec)

	origStartGMBrokerFn := startGMBrokerFn
	t.Cleanup(func() { startGMBrokerFn = origStartGMBrokerFn })
	startGMBrokerFn = func(sockPath string, _ int, _ string) (*gmbroker.Broker, error) {
		b, err := gmbroker.StartWithFetcher(sockPath, func(_ context.Context) (string, error) { return "fake spec", nil })
		if err != nil {
			return nil, err
		}
		b.SetProjectCheckFn(func(cfg gmbroker.ProjectCheckConfig, _ string) (*gmbroker.ProjectCheckResult, error) {
			return &gmbroker.ProjectCheckResult{OK: true, WorkingTreeFingerprint: "pp-test-fp"}, nil
		})
		b.SetComputeFingerprintFn(func(string) (string, error) { return "pp-test-fp", nil })
		b.SetGetOrCreatePendingReviewFn(func(_ gmbroker.ReviewerConfig) (string, error) {
			if state.pendingReviewID == "" {
				state.createCalls++
				state.pendingReviewID = "PRR_1"
			}
			return state.pendingReviewID, nil
		})
		b.SetAddReviewCommentFn(func(_ gmbroker.ReviewerConfig, reviewID, _, _ string, _ int) (string, string, bool, error) {
			state.pendingReviewID = reviewID
			return "comment-1", "thread-1", false, nil
		})
		return b, nil
	}

	var precheckCalls int
	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		precheckCalls++
		if precheckCalls == 1 {
			return writeReviewerPrecheck(t, r, evLogPath, false, "pp-test-fp", "pp-test-fp"), nil
		}
		return writeReviewerPrecheck(t, r, evLogPath, true, "pp-test-fp", "pp-test-fp"), nil
	}

	var reviewerPrompts []string
	r.SetRunAgentFn(func(_ context.Context, cfg agent.RoleConfig) (int, agent.TranscriptPaths, error) {
		switch cfg.Role {
		case "dev":
			if !sendGMProjectCheck(cfg.Env) {
				t.Fatal("dev precheck failed")
			}
			if !sendGMDevDone(cfg.Env) {
				t.Fatal("dev terminal gate failed")
			}
		case "reviewer":
			reviewerPrompts = append(reviewerPrompts, cfg.UserPrompt)
			if len(reviewerPrompts) == 1 {
				result := callGMTool(gmSockFromEnv(cfg.Env), "gm_review_submit_comment", "c1", map[string]any{
					"path":     "main.go",
					"line":     10,
					"body":     "Inline finding",
					"severity": "blocking",
				})
				if result == nil || result["ok"] != true {
					t.Fatalf("gm_review_submit_comment: got %v", result)
				}
				result = callGMTool(gmSockFromEnv(cfg.Env), "gm_review_submit", "c2", map[string]any{
					"verdict":         "approved",
					"mergeConfidence": "high",
					"body":            "Looks good",
				})
				if result == nil || result["ok"] != false || result["code"] != "REVIEWER_GATE" {
					t.Fatalf("expected REVIEWER_GATE rejection, got %v", result)
				}
			} else {
				result := callGMTool(gmSockFromEnv(cfg.Env), "gm_review_submit", "c3", map[string]any{
					"verdict":         "approved",
					"mergeConfidence": "high",
					"body":            "Approved after re-review",
				})
				if result == nil || result["ok"] != true {
					t.Fatalf("expected approved submit on retry, got %v", result)
				}
			}
		}
		return 0, fakeTranscriptPaths("/tmp", cfg.Role), nil
	})

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if len(reviewerPrompts) != 2 {
		t.Fatalf("expected 2 reviewer prompts, got %d", len(reviewerPrompts))
	}
	if !strings.Contains(reviewerPrompts[1], "Previous approval rejected") && !strings.Contains(reviewerPrompts[1], "rejected by the runner") {
		t.Errorf("gate retry prompt missing approval explanation: %s", reviewerPrompts[1])
	}
	if state.createCalls != 1 {
		t.Errorf("expected one pending review creation, got %d", state.createCalls)
	}
	if state.deleteCalls != 0 {
		t.Errorf("expected no pending review deletion on gate retry, got %d", state.deleteCalls)
	}
	if state.submitCalls != 1 {
		t.Errorf("expected one GitHub review submission, got %d", state.submitCalls)
	}
	if state.submitReviewID != state.pendingReviewID {
		t.Errorf("submitted review ID %q did not match preserved pending review %q", state.submitReviewID, state.pendingReviewID)
	}
}

func TestOrchestrate_ReviewerChangesRequested_SubmitsStringReviewIDAndRetriesDev(t *testing.T) {
	exec, state := newReviewerGraphQLExecutor(`[]`)
	r, logPath, stderr := setupPingPongRunner(t, exec)

	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		return writeReviewerPrecheck(t, r, evLogPath, true, "pp-test-fp", "pp-test-fp"), nil
	}

	var reviewerPrompts []string
	var devPrompts []string
	r.SetRunAgentFn(makeReviewerChangesRequestedAgent(t, &reviewerPrompts, &devPrompts))

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeSuccess {
		t.Fatalf("outcome: got %q, want %q; stderr: %s", outcome, outcomeSuccess, stderr.String())
	}
	if len(reviewerPrompts) != 2 {
		t.Fatalf("expected 2 reviewer prompts, got %d", len(reviewerPrompts))
	}
	if len(devPrompts) != 2 {
		t.Fatalf("expected 2 dev prompts, got %d", len(devPrompts))
	}
	if !strings.Contains(devPrompts[1], "Please fix") {
		t.Errorf("dev retry prompt missing review body: %s", devPrompts[1])
	}
	assertLatestReviewSubmittedEvent(t, logPath, "9001", 1)
	if state.submitCalls != 2 {
		t.Errorf("expected 2 GitHub review submissions, got %d", state.submitCalls)
	}
	if state.submitReviewID == "" {
		t.Fatal("expected submitted review ID to be preserved")
	}
	if state.submitReviewID != state.pendingReviewID {
		t.Errorf("submitted review ID %q did not match preserved pending review %q", state.submitReviewID, state.pendingReviewID)
	}
}

func TestOrchestrate_ReviewerInvalidApproval_BoundedToThreeAttempts(t *testing.T) {
	exec, _ := newReviewerGraphQLExecutor(`[]`)
	r, logPath, stderr := setupPingPongRunner(t, exec)

	r.reviewerPrecheckFn = func(_, evLogPath string) (string, error) {
		return writeReviewerPrecheck(t, r, evLogPath, false, "pp-test-fp", "pp-test-fp"), nil
	}

	agentFn, reviewerCalls := makeReviewerInvalidApprovalAgent(t)
	r.SetRunAgentFn(agentFn)

	outcome := runOrchestrate(t, r, logPath)
	if outcome != outcomeReviewFailed {
		t.Fatalf("outcome: got %q, want %q; stderr: %s", outcome, outcomeReviewFailed, stderr.String())
	}
	if *reviewerCalls != 3 {
		t.Errorf("expected exactly 3 reviewer attempts, got %d", *reviewerCalls)
	}
}
