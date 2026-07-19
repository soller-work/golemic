package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/repo"
	"golemic/internal/runloop"
	"golemic/internal/runner"
)

// requireTurnID reads GOLEMIC_TURN_ID from the environment via getenv and
// returns the parsed positive integer. Returns -1 and writes an error to
// stderr if the var is missing or not a positive integer.
func requireTurnID(getenv func(string) string, stderr io.Writer) (int, bool) {
	v := getenv("GOLEMIC_TURN_ID")
	n, err := strconv.Atoi(v)
	if v == "" || err != nil || n <= 0 {
		fmt.Fprintf(stderr, "Missing required environment variable: GOLEMIC_TURN_ID\n") //nolint:errcheck
		return -1, false
	}
	return n, true
}

// readEventsForDedup reads the event log for dedup purposes.
// Returns nil (no events) when the log does not yet exist; returns an error
// only on genuine read failures.
func readEventsForDedup(logPath string) ([]eventlog.Event, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(logPath)
	if err != nil {
		if strings.Contains(err.Error(), "LOG_FILE_NOT_FOUND") {
			return nil, nil
		}
		return nil, err
	}
	return events, nil
}

var knownCommands = []struct {
	name string
	desc string
}{
	{"preflight", "Check prerequisites"},
	{"run", "Run the main process (golemic run --issue N)"},
	{"emit", "Emit an event to the run log"},
	{"open-pr", "Open a pull request"},
	{"submit-review", "Submit a review via GraphQL pending-review flow"},
	{"review-comment", "Add an inline review comment to the pending review via GraphQL"},
	{"status", "Show run health status"},
	{"next-issue", "Return the next takeable GitHub issue (JSON)"},
	{"slice", "Print the authoritative task spec for an issue (golemic slice --issue N)"},
	{"claim-issue", "Claim an issue as in-progress for the dev-bot (golemic claim-issue --number N)"},
	{"release-issue", "Release a claimed issue lock with reason-driven label handoff (golemic release-issue --number N --reason done|failed|abandoned)"},
	{"run-loop", "Run the autonomous 60-second polling loop for takeable issues"},
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage: golemic <command>\n\n") //nolint:errcheck
	fmt.Fprintf(w, "Available commands:\n")        //nolint:errcheck
	for _, c := range knownCommands {
		fmt.Fprintf(w, "  %-13s %s\n", c.name, c.desc) //nolint:errcheck
	}
}

// run dispatches subcommands. All error and usage output goes to stderr.
// stdout is left untouched for error states. Returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int { //nolint:cyclop,gocognit,funlen
	if len(args) < 2 {
		usage(stderr)
		return 1
	}

	command := args[1]

	if command == "preflight" {
		pfs := flag.NewFlagSet("preflight", flag.ContinueOnError)
		pfs.SetOutput(stderr)
		var checkFlag bool
		pfs.BoolVar(&checkFlag, "check", false, "Run in read-only check mode (no scaffolding, local token comparison)")
		if err := pfs.Parse(args[2:]); err != nil {
			return 1
		}

		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "failed to get home directory: %v\n", err) //nolint:errcheck
			return 1
		}

		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "failed to get current directory: %v\n", err) //nolint:errcheck
			return 1
		}

		// Resolve host repo (handles symlinked golemic)
		repoRoot, err := repo.ResolveHostRepo(osExecutor{}, cwd)
		if err != nil {
			fmt.Fprintf(stderr, "failed to resolve host repo: %v\n", err) //nolint:errcheck
			return 1
		}

		return runPreflight(osExecutor{}, homeDir, repoRoot, stdout, stderr, checkFlag)
	}

	if command == "run" {
		return runRun(args, stdout, stderr)
	}

	if command == "emit" {
		return runEmit(args, stdout, stderr, os.Getenv)
	}

	if command == "open-pr" {
		return runOpenPR(args, stdout, stderr, os.Getenv, osExecutor{}, func() (*config.Config, error) {
			return config.Load(".")
		})
	}

	if command == "submit-review" {
		return runSubmitReview(args, stdout, stderr, os.Getenv, osExecutor{})
	}

	if command == "review-comment" {
		return runReviewComment(args, stdout, stderr, os.Getenv, osExecutor{})
	}

	if command == "status" {
		return runStatus(args, stdout, stderr, osExecutor{})
	}

	if command == "next-issue" {
		return runNextIssue(args, stdout, stderr, osExecutor{})
	}

	if command == "slice" {
		return runSlice(args, stdout, stderr, osExecutor{})
	}

	if command == "claim-issue" {
		return runClaimIssue(args, stdout, stderr, os.Getenv, osExecutor{})
	}

	if command == "release-issue" {
		return runReleaseIssue(args, stdout, stderr, os.Getenv, osExecutor{})
	}

	if command == "run-loop" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return runRunLoop(ctx, args, stdout, stderr, osRunLoopExecutor{})
	}

	for _, c := range knownCommands {
		if c.name == command {
			fmt.Fprintln(stderr, "not implemented") //nolint:errcheck
			return 1
		}
	}

	fmt.Fprintf(stderr, "Unknown command: %s\n", command) //nolint:errcheck
	usage(stderr)
	return 1
}

// runPreflight executes the preflight command with injectable dependencies.
// checkMode=false runs setup mode (scaffolds); checkMode=true runs read-only check mode.
func runPreflight(executor preflight.Executor, homeDir, repoRoot string, stdout, stderr io.Writer, checkMode bool) int {
	p := preflight.New(executor, homeDir, repoRoot)
	p.SetStdout(stdout)

	var results preflight.Results
	if checkMode {
		results = p.Check()
	} else {
		results = p.RunAll()
	}

	if results.AllOK() {
		return 0
	}
	return 1
}

// runEmit executes the emit subcommand: golemic emit --type <t> --payload '<json>'
// It reads GOLEMIC_RUN_ID and GOLEMIC_EVENT_LOG from the environment via getenv,
// validates inputs, and appends one event to the JSONL event log.
func runEmit(args []string, stdout, stderr io.Writer, getenv func(string) string) int { //nolint:cyclop,funlen
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var typeFlag string
	var payloadFlag string
	fs.StringVar(&typeFlag, "type", "", "Event type (required)")
	fs.StringVar(&payloadFlag, "payload", "", "Event payload as JSON object (required)")

	// Parse flags from args[2:] (after "golemic emit")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	// Check env vars before any I/O.
	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")

	if runID == "" || eventLogPath == "" {
		var missing []string
		if runID == "" {
			missing = append(missing, "GOLEMIC_RUN_ID")
		}
		if eventLogPath == "" {
			missing = append(missing, "GOLEMIC_EVENT_LOG")
		}
		fmt.Fprintf(stderr, "Missing required environment variable: %s\n", strings.Join(missing, ", ")) //nolint:errcheck
		return 1
	}

	// BR-001: GOLEMIC_TURN_ID is required.
	turnID, ok := requireTurnID(getenv, stderr)
	if !ok {
		return 1
	}

	// BR-001: --type must be non-empty.
	if typeFlag == "" {
		fmt.Fprintln(stderr, "--type must not be empty") //nolint:errcheck
		return 1
	}

	// BR-002: --payload must be valid JSON that decodes to a JSON object.
	var payloadObj interface{}
	if err := json.Unmarshal([]byte(payloadFlag), &payloadObj); err != nil {
		fmt.Fprintf(stderr, "Invalid --payload: %v\n", err) //nolint:errcheck
		return 1
	}

	// Verify it is a JSON object (not array, string, number, or null).
	payloadMap, isObject := payloadObj.(map[string]interface{})
	if !isObject {
		fmt.Fprintf(stderr, "Invalid --payload: JSON value must be an object, got %T\n", payloadObj) //nolint:errcheck
		return 1
	}

	// Re-encode to normalise formatting.
	normalizedPayload, err := json.Marshal(payloadMap)
	if err != nil {
		fmt.Fprintf(stderr, "Invalid --payload: %v\n", err) //nolint:errcheck
		return 1
	}

	// BR-003/BR-004: dedup on (turnId, type) — check before any I/O.
	existingEvents, err := readEventsForDedup(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}
	if eventlog.HasTurnTypeEvent(existingEvents, turnID, typeFlag) {
		fmt.Fprintf(stdout, "already emitted for this turn\n") //nolint:errcheck
		return 0
	}

	// Create writer and append the event.
	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}
	defer writer.Close() //nolint:errcheck

	event := eventlog.Event{
		Type:    typeFlag,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: normalizedPayload,
	}

	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}

	return 0
}

var issueBranchRe = regexp.MustCompile(`^golemic/issue-([1-9][0-9]*)$`)

// ensureBodyClosesIssue appends a "Closes #<N>" line to the PR body when the
// branch is a golemic issue branch (golemic/issue-<N>) and the body does not
// already contain a GitHub closing keyword for that issue. Without a closing
// keyword, merging the PR does not auto-close the issue.
func ensureBodyClosesIssue(body, branch string) string {
	m := issueBranchRe.FindStringSubmatch(branch)
	if m == nil {
		return body
	}
	num := m[1]

	closing := regexp.MustCompile(`(?i)\b(close[sd]?|fix(e[sd])?|resolve[sd]?)\s+#` + num + `\b`)
	if closing.MatchString(body) {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\nCloses #" + num + "\n"
}

// runOpenPR executes the open-pr subcommand: golemic open-pr --title <t> --body <b>
// It validates env var context, resolves the current branch, creates a PR via gh,
// parses the PR number and URL, and writes a pr_opened event atomically.
func runOpenPR(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor, loadConfig func() (*config.Config, error)) int { //nolint:gocognit,cyclop,funlen,maintidx
	fs := flag.NewFlagSet("open-pr", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var titleFlag string
	var bodyFlag string
	fs.StringVar(&titleFlag, "title", "", "PR title (required)")
	fs.StringVar(&bodyFlag, "body", "", "PR body (required)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	// Check env vars before any gh/git call.
	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")

	if runID == "" || eventLogPath == "" {
		var missing []string
		if runID == "" {
			missing = append(missing, "GOLEMIC_RUN_ID")
		}
		if eventLogPath == "" {
			missing = append(missing, "GOLEMIC_EVENT_LOG")
		}
		fmt.Fprintf(stderr, "Missing required environment variable: %s\n", strings.Join(missing, ", ")) //nolint:errcheck
		return 1
	}

	// BR-001: GOLEMIC_TURN_ID is required.
	turnID, ok := requireTurnID(getenv, stderr)
	if !ok {
		return 1
	}

	// Validate --title and --body must be non-empty (IF-001 constraints).
	if titleFlag == "" {
		fmt.Fprintln(stderr, "--title must not be empty") //nolint:errcheck
		return 1
	}
	if bodyFlag == "" {
		fmt.Fprintln(stderr, "--body must not be empty") //nolint:errcheck
		return 1
	}

	// BR-001: Load config before any gh call; abort if missing or invalid.
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "Failed to load config: %v\n", err) //nolint:errcheck
		return 1
	}

	// BR-002, BR-003: Execute verify_command via sh -c before any gh call.
	if _, verifyErr := executor.Run("sh", "-c", cfg.VerifyCommand); verifyErr != nil {
		var ee *preflight.ErrExit
		if errors.As(verifyErr, &ee) {
			fmt.Fprintf(stderr, "verify_command failed: %s\n", strings.TrimSpace(ee.Stderr)) //nolint:errcheck
		} else {
			fmt.Fprintf(stderr, "verify_command failed: %v\n", verifyErr) //nolint:errcheck
		}
		return 1
	}

	// BR-001: Get current branch via git branch --show-current.
	branchOut, err := executor.Run("git", "branch", "--show-current")
	if err != nil {
		fmt.Fprintf(stderr, "Failed to determine current branch: %v\n", err) //nolint:errcheck
		return 1
	}
	branch := strings.TrimSpace(branchOut)
	if branch == "" {
		fmt.Fprintln(stderr, "Failed to determine current branch: detached HEAD or not on a branch") //nolint:errcheck
		return 1
	}

	// Ensure the PR body carries a GitHub closing keyword so the merge
	// auto-closes the originating issue. The issue number is encoded in the
	// golemic branch name (golemic/issue-<N>); non-golemic branches are left as-is.
	body := ensureBodyClosesIssue(bodyFlag, branch)

	// BR-003/BR-004: dedup on (turnId, pr_opened) — check BEFORE gh call.
	existingEvents, err := readEventsForDedup(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to create PR: %v\n", err) //nolint:errcheck
		return 1
	}
	if eventlog.HasTurnTypeEvent(existingEvents, turnID, eventlog.EventPROpened) {
		fmt.Fprintf(stdout, "PR already opened for this turn\n") //nolint:errcheck
		return 0
	}

	// BR-001: Probe for existing open PRs on this branch before calling gh pr create.
	prListOut, err := executor.RunWithEnv(nil, "gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number,url")
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) {
			fmt.Fprintf(stderr, "Failed to list open PRs for branch %s: %s\n", branch, strings.TrimSpace(ee.Stderr)) //nolint:errcheck
		} else {
			fmt.Fprintf(stderr, "Failed to list open PRs for branch %s: %v\n", branch, err) //nolint:errcheck
		}
		return 1
	}
	type prListEntry struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	var openPRs []prListEntry
	if err := json.Unmarshal([]byte(prListOut), &openPRs); err != nil {
		fmt.Fprintf(stderr, "Failed to parse gh pr list output: %v\n", err) //nolint:errcheck
		return 1
	}

	// BR-003: Idempotent path — exactly one open PR exists, skip create.
	if len(openPRs) == 1 {
		existingPR := openPRs[0]
		writer, err := eventlog.NewWriter(eventLogPath)
		if err != nil {
			fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
			return 1
		}
		defer writer.Close() //nolint:errcheck
		payload := map[string]string{
			"prNumber": strconv.Itoa(existingPR.Number),
			"url":      existingPR.URL,
			"branch":   branch,
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
			return 1
		}
		event := eventlog.Event{
			Type:    eventlog.EventPROpened,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   runID,
			TurnID:  turnID,
			Payload: payloadJSON,
		}
		if err := writer.Write(event); err != nil {
			fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
			return 1
		}
		fmt.Fprintln(stdout, existingPR.URL) //nolint:errcheck
		return 0
	}

	// BR-004: More than one open PR — fail fast.
	if len(openPRs) > 1 {
		fmt.Fprintf(stderr, "Branch %s has %d open PRs; expected 0 or 1. Resolve manually before retrying.\n", branch, len(openPRs)) //nolint:errcheck
		return 1
	}

	// BR-002, IC-001: Create PR via gh pr create.
	// GH_TOKEN is inherited from the process environment (BR-005).
	prOut, err := executor.RunWithEnv(
		nil, // no additional env vars; GH_TOKEN comes from process
		"gh", "pr", "create",
		"--title", titleFlag,
		"--body", body,
		"--base", "main",
		"--head", branch,
	)
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) {
			fmt.Fprintf(stderr, "Failed to create PR: %s\n", strings.TrimSpace(ee.Stderr)) //nolint:errcheck
		} else {
			fmt.Fprintf(stderr, "Failed to create PR: %v\n", err) //nolint:errcheck
		}
		return 1
	}

	// Parse PR number and URL from gh output.
	// gh pr create outputs the PR URL on stdout, e.g.:
	//   https://github.com/owner/repo/pull/123
	prURL := strings.TrimSpace(prOut)
	if prURL == "" {
		fmt.Fprintln(stderr, "Failed to parse PR number/URL from gh output: empty output") //nolint:errcheck
		return 1
	}

	// Extract PR number from the last path segment of the URL.
	prNumber := ""
	if idx := strings.LastIndex(prURL, "/"); idx >= 0 {
		candidate := prURL[idx+1:]
		if _, err := strconv.Atoi(candidate); err == nil {
			prNumber = candidate
		}
	}
	if prNumber == "" {
		fmt.Fprintf(stderr, "Failed to parse PR number/URL from gh output: %s\n", prURL) //nolint:errcheck
		return 1
	}

	// Write pr_opened event (SC-002). Event is written only after gh succeeds.
	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}
	defer writer.Close() //nolint:errcheck

	payload := map[string]string{
		"prNumber": prNumber,
		"url":      prURL,
		"branch":   branch,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}

	event := eventlog.Event{
		Type:    eventlog.EventPROpened,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: payloadJSON,
	}

	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}

	// Print PR URL to stdout for the caller.
	fmt.Fprintln(stdout, prURL) //nolint:errcheck
	return 0
}

// ---------------------------------------------------------------------------
// GraphQL helpers shared by submit-review and review-comment
// ---------------------------------------------------------------------------

// GitHub GraphQL mutations and queries used by the review flow.
const (
	discoverReviewQuery = `query($owner:String!,$repo:String!,$prNumber:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$prNumber){id,reviews(first:1,states:[PENDING]){nodes{id,comments{totalCount}}}}}}` //nolint:lll
	createReviewMutation  = `mutation($pullRequestId:ID!){addPullRequestReview(input:{pullRequestId:$pullRequestId}){pullRequestReview{id}}}`
	submitReviewMutation  = `mutation($reviewId:ID!,$event:PullRequestReviewEvent!,$body:String!){submitPullRequestReview(input:{pullRequestReviewId:$reviewId,event:$event,body:$body}){pullRequestReview{id,state}}}` //nolint:lll
	addThreadMutation     = `mutation($reviewId:ID!,$path:String!,$line:Int!,$side:DiffSide!,$body:String!){addPullRequestReviewThread(input:{pullRequestReviewId:$reviewId,path:$path,line:$line,side:$side,body:$body}){thread{id}}}` //nolint:lll
)

// pendingReviewInfo holds the result of discovering or creating a pending review.
type pendingReviewInfo struct {
	reviewID     string
	commentCount int
}

// repoOwnerName returns the GitHub owner login and repository name for the current repo.
func repoOwnerName(executor preflight.Executor) (owner, name string, err error) {
	out, err := executor.RunWithEnv(nil, "gh", "repo", "view", "--json", "owner,name")
	if err != nil {
		return "", "", fmt.Errorf("get repo info: %w", err)
	}
	var v struct {
		Owner struct{ Login string `json:"login"` } `json:"owner"`
		Name  string `json:"name"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &v); jsonErr != nil {
		return "", "", fmt.Errorf("parse repo info: %w", jsonErr)
	}
	if v.Owner.Login == "" || v.Name == "" {
		return "", "", fmt.Errorf("repo info: missing owner or name")
	}
	return v.Owner.Login, v.Name, nil
}

// discoverOrCreatePendingReview returns the viewer's pending review on the PR,
// creating one via addPullRequestReview if none exists (BR-007, IC-001, IC-003).
func discoverOrCreatePendingReview(executor preflight.Executor, owner, repoName string, prNumber int) (pendingReviewInfo, error) { //nolint:funlen
	out, err := executor.RunWithEnv(nil, "gh", "api", "graphql",
		"-f", "query="+discoverReviewQuery,
		"-f", "owner="+owner,
		"-f", "repo="+repoName,
		"-F", "prNumber="+strconv.Itoa(prNumber),
	)
	if err != nil {
		return pendingReviewInfo{}, fmt.Errorf("discover pending review: %w", err)
	}

	var discoverResp struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ID      string `json:"id"`
					Reviews struct {
						Nodes []struct {
							ID       string `json:"id"`
							Comments struct {
								TotalCount int `json:"totalCount"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &discoverResp); jsonErr != nil {
		return pendingReviewInfo{}, fmt.Errorf("parse discover response: %w", jsonErr)
	}

	pr := discoverResp.Data.Repository.PullRequest
	if len(pr.Reviews.Nodes) > 0 {
		node := pr.Reviews.Nodes[0]
		return pendingReviewInfo{reviewID: node.ID, commentCount: node.Comments.TotalCount}, nil
	}

	// No pending review found; create one (IC-001).
	createOut, createErr := executor.RunWithEnv(nil, "gh", "api", "graphql",
		"-f", "query="+createReviewMutation,
		"-f", "pullRequestId="+pr.ID,
	)
	if createErr != nil {
		return pendingReviewInfo{}, fmt.Errorf("create pending review: %w", createErr)
	}

	var createResp struct {
		Data struct {
			AddPullRequestReview struct {
				PullRequestReview struct {
					ID string `json:"id"`
				} `json:"pullRequestReview"`
			} `json:"addPullRequestReview"`
		} `json:"data"`
	}
	if jsonErr := json.Unmarshal([]byte(createOut), &createResp); jsonErr != nil {
		return pendingReviewInfo{}, fmt.Errorf("parse create response: %w", jsonErr)
	}

	reviewID := createResp.Data.AddPullRequestReview.PullRequestReview.ID
	if reviewID == "" {
		return pendingReviewInfo{}, fmt.Errorf("create pending review: empty review ID in response")
	}
	return pendingReviewInfo{reviewID: reviewID, commentCount: 0}, nil
}

// addReviewThread adds one inline review thread to the given pending review (IC-002).
func addReviewThread(executor preflight.Executor, reviewID, path string, line int, side, body string) error {
	_, err := executor.RunWithEnv(nil, "gh", "api", "graphql",
		"-f", "query="+addThreadMutation,
		"-f", "reviewId="+reviewID,
		"-f", "path="+path,
		"-F", "line="+strconv.Itoa(line),
		"-f", "side="+side,
		"-f", "body="+body,
	)
	return err
}

// isAnchorError returns true when the error text indicates the line/path/side is not in the diff.
func isAnchorError(errText string) bool {
	lower := strings.ToLower(errText)
	return strings.Contains(lower, "not part of the diff") ||
		strings.Contains(lower, "line must be part") ||
		strings.Contains(lower, "invalid side")
}

// runReviewComment executes the review-comment subcommand:
// golemic review-comment --pr <n> --path <p> --line <l> --body <b> [--side RIGHT|LEFT] [--start-line <s>]
// Adds one inline review thread to the viewer's pending review via GraphQL addPullRequestReviewThread.
// Exit 2 on anchor failure (ANCHOR_FAILED on stderr); exit 1 on other errors.
func runReviewComment(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int { //nolint:cyclop,funlen
	fs := flag.NewFlagSet("review-comment", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var prFlag, lineFlag, startLineFlag int
	var pathFlag, bodyFlag, sideFlag string
	fs.IntVar(&prFlag, "pr", 0, "PR number (required)")
	fs.StringVar(&pathFlag, "path", "", "Repo-relative file path (required)")
	fs.IntVar(&lineFlag, "line", 0, "Line number on the given side (required)")
	fs.IntVar(&startLineFlag, "start-line", 0, "Start line for multi-line comment (contract completeness; out of scope for reviewer prompt in this slice)")
	fs.StringVar(&sideFlag, "side", "RIGHT", "Diff side: RIGHT or LEFT (default RIGHT)")
	fs.StringVar(&bodyFlag, "body", "", "Comment body (required)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	_ = startLineFlag // multi-line comments are out of scope per the slice spec
	_ = stdout        // review-comment writes no stdout on success

	// BR-004: fail-fast on missing env vars (no gh call).
	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")
	if runID == "" || eventLogPath == "" {
		var missing []string
		if runID == "" {
			missing = append(missing, "GOLEMIC_RUN_ID")
		}
		if eventLogPath == "" {
			missing = append(missing, "GOLEMIC_EVENT_LOG")
		}
		fmt.Fprintf(stderr, "Missing required environment variable: %s\n", strings.Join(missing, ", ")) //nolint:errcheck
		return 1
	}

	// Validate flags.
	if prFlag <= 0 {
		fmt.Fprintln(stderr, "--pr must be a positive integer") //nolint:errcheck
		return 1
	}
	if pathFlag == "" {
		fmt.Fprintln(stderr, "--path must not be empty") //nolint:errcheck
		return 1
	}
	if lineFlag <= 0 {
		fmt.Fprintln(stderr, "--line must be a positive integer") //nolint:errcheck
		return 1
	}
	if bodyFlag == "" {
		fmt.Fprintln(stderr, "--body must not be empty") //nolint:errcheck
		return 1
	}
	if sideFlag != "RIGHT" && sideFlag != "LEFT" {
		fmt.Fprintf(stderr, "Invalid --side: must be RIGHT or LEFT, got %q\n", sideFlag) //nolint:errcheck
		return 1
	}

	// Get repo context (owner/name) for the GraphQL query.
	owner, repoName, err := repoOwnerName(executor)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to add review comment: %v\n", err) //nolint:errcheck
		return 1
	}

	// Discover or create pending review (BR-007).
	info, err := discoverOrCreatePendingReview(executor, owner, repoName, prFlag)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to add review comment: %v\n", err) //nolint:errcheck
		return 1
	}

	// Add inline review thread (IC-002).
	if threadErr := addReviewThread(executor, info.reviewID, pathFlag, lineFlag, sideFlag, bodyFlag); threadErr != nil {
		var ee *preflight.ErrExit
		if errors.As(threadErr, &ee) && isAnchorError(ee.Stderr) {
			// BR-002: anchor failure → exit 2 + ANCHOR_FAILED on stderr.
			fmt.Fprintf(stderr, "ANCHOR_FAILED: path=%s line=%d side=%s reason=%s\n", //nolint:errcheck
				pathFlag, lineFlag, sideFlag, strings.TrimSpace(ee.Stderr))
			return 2
		}
		fmt.Fprintf(stderr, "Failed to add review comment: %v\n", threadErr) //nolint:errcheck
		return 1
	}

	return 0
}

// runSubmitReview executes the submit-review subcommand:
// golemic submit-review --verdict approved|changes_requested --body <text> --pr <n> --merge-confidence high|low
// Uses the GraphQL pending-review flow: discover-or-create pending review, submitPullRequestReview.
// Writes a review_submitted event atomically (only on GraphQL success).
func runSubmitReview(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int { //nolint:gocognit,cyclop,funlen
	fs := flag.NewFlagSet("submit-review", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var verdictFlag string
	var bodyFlag string
	var prFlag int
	var mergeConfidenceFlag string
	fs.StringVar(&verdictFlag, "verdict", "", "Verdict: 'approved' or 'changes_requested' (required)")
	fs.StringVar(&bodyFlag, "body", "", "Review body (required)")
	fs.IntVar(&prFlag, "pr", 0, "PR number (required)")
	fs.StringVar(&mergeConfidenceFlag, "merge-confidence", "", "Merge confidence: 'high' or 'low' (required)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	// Check env vars before any gh/git call.
	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")

	if runID == "" || eventLogPath == "" {
		var missing []string
		if runID == "" {
			missing = append(missing, "GOLEMIC_RUN_ID")
		}
		if eventLogPath == "" {
			missing = append(missing, "GOLEMIC_EVENT_LOG")
		}
		fmt.Fprintf(stderr, "Missing required environment variable: %s\n", strings.Join(missing, ", ")) //nolint:errcheck
		return 1
	}

	// BR-001: GOLEMIC_TURN_ID is required.
	turnID, ok := requireTurnID(getenv, stderr)
	if !ok {
		return 1
	}

	// BR-009: Validate --merge-confidence fail-fast before any gh call.
	if mergeConfidenceFlag != "high" && mergeConfidenceFlag != "low" {
		fmt.Fprintf(stderr, "Invalid merge confidence: must be 'high' or 'low', got %q\n", mergeConfidenceFlag) //nolint:errcheck
		return 1
	}

	// Validate verdict before any gh call (fail-fast).
	if verdictFlag != "approved" && verdictFlag != "changes_requested" {
		fmt.Fprintf(stderr, "Invalid verdict: must be 'approved' or 'changes_requested', got %q\n", verdictFlag) //nolint:errcheck
		return 1
	}

	// Validate required flags.
	if bodyFlag == "" {
		fmt.Fprintln(stderr, "--body must not be empty") //nolint:errcheck
		return 1
	}
	if prFlag <= 0 {
		fmt.Fprintln(stderr, "--pr must be a positive integer") //nolint:errcheck
		return 1
	}

	// Validate event log path is writable BEFORE calling gh (atomic coupling: fail-closed).
	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}
	defer writer.Close() //nolint:errcheck

	// BR-003/BR-004: dedup on (turnId, review_submitted) — check BEFORE gh call.
	existingEvents, err := readEventsForDedup(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: %v\n", err) //nolint:errcheck
		return 1
	}
	if eventlog.HasTurnTypeEvent(existingEvents, turnID, eventlog.EventReviewSubmitted) {
		fmt.Fprintf(stdout, "review already submitted for this turn\n") //nolint:errcheck
		return 0
	}

	// BR-001: Get repo context for GraphQL queries.
	owner, repoName, err := repoOwnerName(executor)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: %v\n", err) //nolint:errcheck
		return 1
	}

	// BR-007: Discover or create pending review (IC-001 + IC-003).
	info, err := discoverOrCreatePendingReview(executor, owner, repoName, prFlag)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: %v\n", err) //nolint:errcheck
		return 1
	}

	// BR-001: Submit the pending review via GraphQL (IC-004).
	ghEvent := "APPROVE"
	if verdictFlag == "changes_requested" {
		ghEvent = "REQUEST_CHANGES"
	}

	submitOut, submitErr := executor.RunWithEnv(nil, "gh", "api", "graphql",
		"-f", "query="+submitReviewMutation,
		"-f", "reviewId="+info.reviewID,
		"-f", "event="+ghEvent,
		"-f", "body="+bodyFlag,
	)
	if submitErr != nil {
		var ee *preflight.ErrExit
		if errors.As(submitErr, &ee) {
			fmt.Fprintf(stderr, "Failed to submit review: %s\n", strings.TrimSpace(ee.Stderr)) //nolint:errcheck
		} else {
			fmt.Fprintf(stderr, "Failed to submit review: %v\n", submitErr) //nolint:errcheck
		}
		return 1
	}

	// Parse reviewId from submit response (BR-006).
	var submitResp struct {
		Data struct {
			SubmitPullRequestReview struct {
				PullRequestReview struct {
					ID string `json:"id"`
				} `json:"pullRequestReview"`
			} `json:"submitPullRequestReview"`
		} `json:"data"`
	}
	if jsonErr := json.Unmarshal([]byte(submitOut), &submitResp); jsonErr != nil {
		fmt.Fprintf(stderr, "Failed to submit review: parse response: %v\n", jsonErr) //nolint:errcheck
		return 1
	}
	reviewID := submitResp.Data.SubmitPullRequestReview.PullRequestReview.ID
	if reviewID == "" {
		fmt.Fprintln(stderr, "Failed to submit review: empty reviewId in response") //nolint:errcheck
		return 1
	}

	// Write review_submitted event (only reached if GraphQL succeeds). SC-003.
	payload := map[string]interface{}{
		"verdict":            verdictFlag,
		"body":               bodyFlag,
		"prNumber":           prFlag,
		"mergeConfidence":    mergeConfidenceFlag,
		"reviewId":           reviewID,
		"inlineCommentCount": info.commentCount,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}

	event := eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: payloadJSON,
	}

	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}

	// Mirror confidence as PR label. Label is created on demand; the event
	// payload is the authoritative source so a label failure is reported but not fatal.
	confidenceLabel := "confidence:" + mergeConfidenceFlag
	// Ignore error: label may already exist.
	_, _ = executor.RunWithEnv(nil, "gh", "label", "create", confidenceLabel, "--color", "0075ca", "--description", "merge confidence")
	if _, err := executor.RunWithEnv(nil, "gh", "pr", "edit", strconv.Itoa(prFlag), "--add-label", confidenceLabel); err != nil {
		fmt.Fprintf(stderr, "Review submitted but PR label could not be set: %v\n", err) //nolint:errcheck
		return 1
	}

	return 0
}

// runRun executes the run subcommand: golemic run --issue <N>
// It parses the --issue flag, resolves the host repo, loads config and credentials,
// generates a runId, creates the event log, writes run_started, loads the issue,
// and performs collision checks.
func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var issueNum int
	var cleanFlag bool
	var quietFlag bool
	fs.IntVar(&issueNum, "issue", 0, "GitHub issue number (required)")
	fs.BoolVar(&cleanFlag, "clean", false, "Remove leftover artifacts for the issue before running")
	fs.BoolVar(&quietFlag, "quiet", false, "Suppress the run-setup header")
	fs.BoolVar(&quietFlag, "q", false, "Suppress the run-setup header (shorthand)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	if issueNum <= 0 {
		fmt.Fprintln(stderr, "--issue must be a positive integer") //nolint:errcheck
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err) //nolint:errcheck
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get current directory: %v\n", err) //nolint:errcheck
		return 1
	}

	r := runner.New(osExecutor{}, homeDir, cwd, issueNum)
	r.SetStdout(stdout)
	r.SetStderr(stderr)
	r.SetClean(cleanFlag)
	r.SetQuiet(quietFlag)
	return r.Run()
}

// osExecutor is the production executor that runs real commands.
type osExecutor struct{}

func (e osExecutor) Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stderr: string(exitErr.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

func (e osExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stderr: string(exitErr.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

func (e osExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stderr: string(exitErr.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

func (e osExecutor) RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stderr: string(exitErr.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

// runRunLoop executes the run-loop subcommand. It resolves the host repo,
// loads config and credentials, verifies preflight labels, then runs the
// autonomous tick loop until ctx is cancelled (SIGINT or SIGTERM in production).
func runRunLoop(ctx context.Context, _ []string, _, stderr io.Writer, executor runloop.Executor) int {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err) //nolint:errcheck
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err) //nolint:errcheck
		return 1
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err) //nolint:errcheck
		return 1
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err) //nolint:errcheck
		return 1
	}

	if _, err := credentials.NewLoader(homeDir).Load(cfg.Project); err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err) //nolint:errcheck
		return 1
	}

	if _, err := executor.RunInDir(repoRoot, "golemic", "preflight", "--check"); err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: preflight check: %v\n", err) //nolint:errcheck
		return 1
	}

	l := runloop.New(executor, homeDir, repoRoot, cfg.Project, stderr)
	l.Run(ctx)
	return 0
}

// osRunLoopExecutor wraps osExecutor and adds subprocess lifecycle support for
// the runner via StartWithEnvInDir.
type osRunLoopExecutor struct {
	osExecutor
}

// osProcessHandle wraps an exec.Cmd and implements runloop.ProcessHandle.
type osProcessHandle struct {
	cmd *exec.Cmd
}

func (h *osProcessHandle) Wait() error                { return h.cmd.Wait() }
func (h *osProcessHandle) Signal(sig os.Signal) error { return h.cmd.Process.Signal(sig) }

func (e osRunLoopExecutor) StartWithEnvInDir(env map[string]string, dir, name string, args ...string) (runloop.ProcessHandle, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcessHandle{cmd: cmd}, nil
}

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}
