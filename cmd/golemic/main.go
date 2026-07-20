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
		fmt.Fprintf(stderr, "Missing required environment variable: GOLEMIC_TURN_ID\n")
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
	{"review-comment", "Add an inline review comment to the pending review (reviewer agent)"},
	{"submit-review", "Submit a review"},
	{"status", "Show run health status"},
	{"next-issue", "Return the next takeable GitHub issue (JSON)"},
	{"slice", "Print the authoritative task spec for an issue (golemic slice --issue N)"},
	{"claim-issue", "Claim an issue as in-progress for the dev-bot (golemic claim-issue --number N)"},
	{"release-issue", "Release a claimed issue lock with reason-driven label handoff (golemic release-issue --number N --reason done|failed|abandoned)"},
	{"run-loop", "Run the autonomous 60-second polling loop for takeable issues"},
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage: golemic <command>\n\n")
	fmt.Fprintf(w, "Available commands:\n")
	for _, c := range knownCommands {
		fmt.Fprintf(w, "  %-13s %s\n", c.name, c.desc)
	}
}

// run dispatches subcommands. All error and usage output goes to stderr.
// stdout is left untouched for error states. Returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		usage(stderr)
		return 1
	}

	command := args[1]

	if command == "preflight" {
		return dispatchPreflight(args, stdout, stderr)
	}

	if code, ok := dispatchCoreCommands(command, args, stdout, stderr); ok {
		return code
	}

	if code, ok := dispatchExtendedCommands(command, args, stdout, stderr); ok {
		return code
	}

	if command == "run-loop" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return runRunLoop(ctx, args, stdout, stderr, osRunLoopExecutor{})
	}

	for _, c := range knownCommands {
		if c.name == command {
			fmt.Fprintln(stderr, "not implemented")
			return 1
		}
	}

	fmt.Fprintf(stderr, "Unknown command: %s\n", command)
	usage(stderr)
	return 1
}

// dispatchPreflight handles the preflight subcommand with its inline flag parsing.
func dispatchPreflight(args []string, stdout, stderr io.Writer) int {
	pfs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	pfs.SetOutput(stderr)
	var checkFlag bool
	pfs.BoolVar(&checkFlag, "check", false, "Run in read-only check mode (no scaffolding, local token comparison)")
	if err := pfs.Parse(args[2:]); err != nil {
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get current directory: %v\n", err)
		return 1
	}

	repoRoot, err := repo.ResolveHostRepo(osExecutor{}, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "failed to resolve host repo: %v\n", err)
		return 1
	}

	return runPreflight(osExecutor{}, homeDir, repoRoot, stdout, stderr, checkFlag)
}

// dispatchCoreCommands handles the five most common subcommands.
func dispatchCoreCommands(command string, args []string, stdout, stderr io.Writer) (int, bool) {
	switch command {
	case "run":
		return runRun(args, stdout, stderr), true
	case "emit":
		return runEmit(args, stdout, stderr, os.Getenv), true
	case "open-pr":
		return runOpenPR(args, stdout, stderr, os.Getenv, osExecutor{}, func() (*config.Config, error) {
			return config.Load(".")
		}), true
	case "review-comment":
		return runReviewComment(args, stdout, stderr, os.Getenv, osExecutor{}), true
	case "submit-review":
		return runSubmitReview(args, stdout, stderr, os.Getenv, osExecutor{}), true
	}
	return 0, false
}

// dispatchExtendedCommands handles the remaining non-loop subcommands.
func dispatchExtendedCommands(command string, args []string, stdout, stderr io.Writer) (int, bool) {
	switch command {
	case "status":
		return runStatus(args, stdout, stderr, osExecutor{}), true
	case "next-issue":
		return runNextIssue(args, stdout, stderr, osExecutor{}), true
	case "slice":
		return runSlice(args, stdout, stderr, osExecutor{}), true
	case "claim-issue":
		return runClaimIssue(args, stdout, stderr, os.Getenv, osExecutor{}), true
	case "release-issue":
		return runReleaseIssue(args, stdout, stderr, os.Getenv, osExecutor{}), true
	}
	return 0, false
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
func runEmit(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var typeFlag string
	var payloadFlag string
	fs.StringVar(&typeFlag, "type", "", "Event type (required)")
	fs.StringVar(&payloadFlag, "payload", "", "Event payload as JSON object (required)")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")
	if !validateRunEnvVars(runID, eventLogPath, stderr) {
		return 1
	}

	turnID, ok := requireTurnID(getenv, stderr)
	if !ok {
		return 1
	}

	normalizedPayload, ok := parseAndNormalizePayload(typeFlag, payloadFlag, stderr)
	if !ok {
		return 1
	}

	// BR-003/BR-004: dedup on (turnId, type) — check before any I/O.
	existingEvents, err := readEventsForDedup(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	if eventlog.HasTurnTypeEvent(existingEvents, turnID, typeFlag) {
		fmt.Fprintf(stdout, "already emitted for this turn\n")
		return 0
	}

	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	defer writer.Close()

	event := eventlog.Event{
		Type:    typeFlag,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: normalizedPayload,
	}
	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
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

// openPRSetup holds the validated inputs needed to create a PR.
type openPRSetup struct {
	runID, eventLogPath string
	turnID              int
	title, body, branch string
}

// prepareOpenPRContext parses and validates all pre-flight state for open-pr:
// flags, env vars, config, verify_command, and current branch.
func prepareOpenPRContext(args []string, getenv func(string) string, executor preflight.Executor, loadConfig func() (*config.Config, error), stderr io.Writer) (*openPRSetup, bool) {
	fs := flag.NewFlagSet("open-pr", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var title, body string
	fs.StringVar(&title, "title", "", "PR title (required)")
	fs.StringVar(&body, "body", "", "PR body (required)")
	if err := fs.Parse(args[2:]); err != nil {
		return nil, false
	}

	// Env vars checked before flag validation (test: TestRunOpenPR_EnvVarsCheckedBeforeValidation).
	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")
	if !validateRunEnvVars(runID, eventLogPath, stderr) {
		return nil, false
	}

	turnID, ok := requireTurnID(getenv, stderr)
	if !ok {
		return nil, false
	}

	if title == "" {
		fmt.Fprintln(stderr, "--title must not be empty")
		return nil, false
	}
	if body == "" {
		fmt.Fprintln(stderr, "--body must not be empty")
		return nil, false
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "Failed to load config: %v\n", err)
		return nil, false
	}

	if _, verifyErr := executor.Run("sh", "-c", cfg.VerifyCommand); verifyErr != nil {
		fmt.Fprintf(stderr, "verify_command failed: %s\n", formatGHError(verifyErr))
		return nil, false
	}

	branchOut, err := executor.Run("git", "branch", "--show-current")
	if err != nil {
		fmt.Fprintf(stderr, "Failed to determine current branch: %v\n", err)
		return nil, false
	}
	branch := strings.TrimSpace(branchOut)
	if branch == "" {
		fmt.Fprintln(stderr, "Failed to determine current branch: detached HEAD or not on a branch")
		return nil, false
	}

	return &openPRSetup{runID: runID, eventLogPath: eventLogPath, turnID: turnID, title: title, body: body, branch: branch}, true
}

// probeOpenPRs lists open PRs on the branch. Returns (entries, true) on success.
func probeOpenPRs(executor preflight.Executor, branch string, stderr io.Writer) ([]prListEntry, bool) {
	out, err := executor.RunWithEnv(nil, "gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number,url")
	if err != nil {
		fmt.Fprintf(stderr, "Failed to list open PRs for branch %s: %s\n", branch, formatGHError(err))
		return nil, false
	}
	var openPRs []prListEntry
	if err := json.Unmarshal([]byte(out), &openPRs); err != nil {
		fmt.Fprintf(stderr, "Failed to parse gh pr list output: %v\n", err)
		return nil, false
	}
	return openPRs, true
}

// recordExistingPR opens the event log and writes a pr_opened event for an already-open PR.
func recordExistingPR(existing prListEntry, setup *openPRSetup, stdout, stderr io.Writer) int {
	writer, err := eventlog.NewWriter(setup.eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	defer writer.Close()

	payload := map[string]string{
		"prNumber": strconv.Itoa(existing.Number),
		"url":      existing.URL,
		"branch":   setup.branch,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	event := eventlog.Event{
		Type:    eventlog.EventPROpened,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   setup.runID,
		TurnID:  setup.turnID,
		Payload: payloadJSON,
	}
	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, existing.URL)
	return 0
}

// createAndRecordNewPR creates a PR via gh pr create, then opens the event log and
// writes the pr_opened event. The event log is opened only after gh succeeds.
func createAndRecordNewPR(executor preflight.Executor, setup *openPRSetup, stdout, stderr io.Writer) int {
	prOut, err := executor.RunWithEnv(
		nil,
		"gh", "pr", "create",
		"--title", setup.title,
		"--body", setup.body,
		"--base", "main",
		"--head", setup.branch,
	)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to create PR: %s\n", formatGHError(err))
		return 1
	}

	prURL := strings.TrimSpace(prOut)
	prNumber, ok := parsePRNumberFromURL(prURL, stderr)
	if !ok {
		return 1
	}

	writer, err := eventlog.NewWriter(setup.eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	defer writer.Close()

	if !recordPROpenedEvent(writer, setup, prNumber, prURL, stderr) {
		return 1
	}
	fmt.Fprintln(stdout, prURL)
	return 0
}

// prListEntry is the JSON shape returned by gh pr list.
type prListEntry struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// runOpenPR executes the open-pr subcommand: golemic open-pr --title <t> --body <b>
// It validates env var context, resolves the current branch, creates a PR via gh,
// parses the PR number and URL, and writes a pr_opened event atomically.
func runOpenPR(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor, loadConfig func() (*config.Config, error)) int {
	setup, ok := prepareOpenPRContext(args, getenv, executor, loadConfig, stderr)
	if !ok {
		return 1
	}

	setup.body = ensureBodyClosesIssue(setup.body, setup.branch)

	existingEvents, err := readEventsForDedup(setup.eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to create PR: %v\n", err)
		return 1
	}
	if eventlog.HasTurnTypeEvent(existingEvents, setup.turnID, eventlog.EventPROpened) {
		fmt.Fprintf(stdout, "PR already opened for this turn\n")
		return 0
	}

	openPRs, ok := probeOpenPRs(executor, setup.branch, stderr)
	if !ok {
		return 1
	}

	if len(openPRs) > 1 {
		fmt.Fprintf(stderr, "Branch %s has %d open PRs; expected 0 or 1. Resolve manually before retrying.\n", setup.branch, len(openPRs))
		return 1
	}

	if len(openPRs) == 1 {
		return recordExistingPR(openPRs[0], setup, stdout, stderr)
	}
	return createAndRecordNewPR(executor, setup, stdout, stderr)
}

// ---------------------------------------------------------------------------
// GraphQL constants for the reviewer review flow (IC-001 through IC-004)
// ---------------------------------------------------------------------------

// graphqlDiscoverReview queries viewer login, PR node ID, and viewer's PENDING reviews.
const graphqlDiscoverReview = `query($owner:String!,$name:String!,$prNumber:Int!){viewer{login}repository(owner:$owner,name:$name){pullRequest(number:$prNumber){id reviews(first:10,states:[PENDING]){nodes{id author{login}}}}}}`

// graphqlCreateReview creates an empty pending review on a PR (IC-001).
const graphqlCreateReview = `mutation($prId:ID!){addPullRequestReview(input:{pullRequestId:$prId}){pullRequestReview{id}}}`

// graphqlAddReviewThread adds an inline thread to a pending review (IC-002).
const graphqlAddReviewThread = `mutation($reviewId:ID!,$path:String!,$line:Int!,$side:DiffSide!,$body:String!){addPullRequestReviewThread(input:{pullRequestReviewId:$reviewId,path:$path,line:$line,side:$side,body:$body}){thread{id}}}`

// graphqlAddReviewThreadWithStart adds an inline thread with a start line (IC-002).
const graphqlAddReviewThreadWithStart = `mutation($reviewId:ID!,$path:String!,$line:Int!,$startLine:Int!,$side:DiffSide!,$body:String!){addPullRequestReviewThread(input:{pullRequestReviewId:$reviewId,path:$path,line:$line,startLine:$startLine,side:$side,body:$body}){thread{id}}}`

// graphqlSubmitReview submits a pending review with verdict and body (IC-004).
const graphqlSubmitReview = `mutation($reviewId:ID!,$event:PullRequestReviewEvent!,$body:String!){submitPullRequestReview(input:{pullRequestReviewId:$reviewId,event:$event,body:$body}){pullRequestReview{id comments{totalCount}}}}`

// getRepoContext returns the owner and name of the current repository via gh.
func getRepoContext(executor preflight.Executor) (owner, name string, err error) {
	out, err := executor.RunWithEnv(nil, "gh", "repo", "view", "--json", "owner,name")
	if err != nil {
		return "", "", fmt.Errorf("failed to get repo context: %w", err)
	}
	var repo struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &repo); err != nil {
		return "", "", fmt.Errorf("failed to parse repo context: %w", err)
	}
	if repo.Owner.Login == "" || repo.Name == "" {
		return "", "", fmt.Errorf("repo context missing owner or name")
	}
	return repo.Owner.Login, repo.Name, nil
}

// createNewPendingReview creates a pending review for prID and returns the new review ID.
func createNewPendingReview(executor preflight.Executor, prID string) (string, error) {
	createOut, err := executor.RunWithEnv(nil, "gh", "api", "graphql",
		"-f", "query="+graphqlCreateReview,
		"-f", "prId="+prID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create pending review: %w", err)
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
	if err := json.Unmarshal([]byte(createOut), &createResp); err != nil {
		return "", fmt.Errorf("failed to parse create review response: %w", err)
	}
	newID := createResp.Data.AddPullRequestReview.PullRequestReview.ID
	if newID == "" {
		return "", fmt.Errorf("create pending review returned empty id")
	}
	return newID, nil
}

// discoverOrCreatePendingReview implements IC-003 then IC-001:
// finds the viewer's existing PENDING review on the PR or creates one.
// Returns the pending review node ID and the PR node ID.
func discoverOrCreatePendingReview(executor preflight.Executor, owner, repoName string, prNumber int) (reviewID, prNodeID string, err error) {
	out, err := executor.RunWithEnv(nil, "gh", "api", "graphql",
		"-f", "query="+graphqlDiscoverReview,
		"-f", "owner="+owner,
		"-f", "name="+repoName,
		"-F", fmt.Sprintf("prNumber=%d", prNumber),
	)
	if err != nil {
		return "", "", fmt.Errorf("failed to discover pending review: %w", err)
	}

	var discoverResp struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Repository struct {
				PullRequest struct {
					ID      string `json:"id"`
					Reviews struct {
						Nodes []struct {
							ID     string `json:"id"`
							Author struct {
								Login string `json:"login"`
							} `json:"author"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &discoverResp); err != nil {
		return "", "", fmt.Errorf("failed to parse discover response: %w", err)
	}

	viewerLogin := discoverResp.Data.Viewer.Login
	prID := discoverResp.Data.Repository.PullRequest.ID
	if prID == "" {
		return "", "", fmt.Errorf("PR #%d not found in repo %s/%s", prNumber, owner, repoName)
	}

	for _, node := range discoverResp.Data.Repository.PullRequest.Reviews.Nodes {
		if node.Author.Login == viewerLogin {
			return node.ID, prID, nil
		}
	}

	newID, err := createNewPendingReview(executor, prID)
	if err != nil {
		return "", "", err
	}
	return newID, prID, nil
}

// isAnchorError returns true when the error message indicates the line/path/side
// is not in the diff (DT-001 row 1, BR-002).
func isAnchorError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "part of the diff") ||
		strings.Contains(lower, "pull request review thread")
}

// buildAddThreadArgs builds the gh api graphql args for adding a review thread.
func buildAddThreadArgs(reviewID, pathFlag, sideFlag, bodyFlag string, lineFlag, startLineFlag int) []string {
	if startLineFlag > 0 {
		return []string{
			"api", "graphql",
			"-f", "query=" + graphqlAddReviewThreadWithStart,
			"-f", "reviewId=" + reviewID,
			"-f", "path=" + pathFlag,
			"-F", fmt.Sprintf("line=%d", lineFlag),
			"-F", fmt.Sprintf("startLine=%d", startLineFlag),
			"-f", "side=" + sideFlag,
			"-f", "body=" + bodyFlag,
		}
	}
	return []string{
		"api", "graphql",
		"-f", "query=" + graphqlAddReviewThread,
		"-f", "reviewId=" + reviewID,
		"-f", "path=" + pathFlag,
		"-F", fmt.Sprintf("line=%d", lineFlag),
		"-f", "side=" + sideFlag,
		"-f", "body=" + bodyFlag,
	}
}

// handleAnchorError formats and returns the appropriate exit code for a thread-add failure.
func handleAnchorError(err error, pathFlag, sideFlag string, lineFlag int, stderr io.Writer) int {
	var ee *preflight.ErrExit
	if errors.As(err, &ee) && isAnchorError(ee.Stderr) {
		fmt.Fprintf(stderr, "ANCHOR_FAILED: path=%s line=%d side=%s reason=%s\n",
			pathFlag, lineFlag, sideFlag, strings.TrimSpace(ee.Stderr))
		return 2
	}
	fmt.Fprintf(stderr, "Failed to add review comment: %s\n", formatGHError(err))
	return 1
}

// runReviewComment executes the review-comment subcommand:
// golemic review-comment --pr <n> --path <p> --line <n> [--start-line <n>] [--side RIGHT|LEFT] --body <text>
// Exits 0 on success, 2 on ANCHOR_FAILED, 1 on other errors. No event is written.
func runReviewComment(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int {
	fs := flag.NewFlagSet("review-comment", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var prFlag int
	var pathFlag string
	var lineFlag int
	var startLineFlag int
	var sideFlag string
	var bodyFlag string
	fs.IntVar(&prFlag, "pr", 0, "PR number (required)")
	fs.StringVar(&pathFlag, "path", "", "Repo-relative file path (required)")
	fs.IntVar(&lineFlag, "line", 0, "Line number on given side (required)")
	fs.IntVar(&startLineFlag, "start-line", 0, "Start line for multi-line comment (optional, must be < --line)")
	fs.StringVar(&sideFlag, "side", "RIGHT", "Diff side: RIGHT or LEFT (default RIGHT)")
	fs.StringVar(&bodyFlag, "body", "", "Comment body (required)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")
	if !validateRunEnvVars(runID, eventLogPath, stderr) {
		return 1
	}

	if !validateReviewCommentInputs(prFlag, lineFlag, pathFlag, sideFlag, bodyFlag, startLineFlag, stderr) {
		return 1
	}

	owner, repoName, err := getRepoContext(executor)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to add review comment: %v\n", err)
		return 1
	}

	reviewID, _, err := discoverOrCreatePendingReview(executor, owner, repoName, prFlag)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to add review comment: %v\n", err)
		return 1
	}

	threadArgs := buildAddThreadArgs(reviewID, pathFlag, sideFlag, bodyFlag, lineFlag, startLineFlag)
	_, err = executor.RunWithEnv(nil, "gh", threadArgs...)
	if err != nil {
		return handleAnchorError(err, pathFlag, sideFlag, lineFlag, stderr)
	}

	_ = stdout // review-comment writes no output on success
	return 0
}

// submitAndRecordReview calls gh to submit the review then writes the event and sets the label.
func submitAndRecordReview(executor preflight.Executor, writer *eventlog.Writer, reviewID, verdictFlag, bodyFlag, mergeConfidenceFlag, runID string, prFlag, turnID int, stdout, stderr io.Writer) int {
	ghEvent := "APPROVE"
	if verdictFlag == "changes_requested" {
		ghEvent = "REQUEST_CHANGES"
	}

	submitOut, err := executor.RunWithEnv(nil, "gh", "api", "graphql",
		"-f", "query="+graphqlSubmitReview,
		"-f", "reviewId="+reviewID,
		"-f", "event="+ghEvent,
		"-f", "body="+bodyFlag,
	)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: %s\n", formatGHError(err))
		return 1
	}

	submittedReviewID, inlineCount, ok := parseSubmitReviewResponse(submitOut, stderr)
	if !ok {
		return 1
	}

	if !recordReviewSubmittedEvent(writer, verdictFlag, bodyFlag, mergeConfidenceFlag, submittedReviewID, runID, prFlag, turnID, inlineCount, stderr) {
		return 1
	}

	if !setMergeConfidenceLabel(executor, mergeConfidenceFlag, prFlag, stderr) {
		return 1
	}

	_ = stdout
	return 0
}

// runSubmitReview executes the submit-review subcommand:
// golemic submit-review --verdict approved|changes_requested --body <text> --pr <n> --merge-confidence high|low
// It validates env var context and verdict (fail-fast), then uses GraphQL to discover-or-create
// a pending review and submit it, writing a review_submitted event atomically (only on gh success).
func runSubmitReview(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int {
	flags, ok := parseSubmitReviewFlags(args, stderr)
	if !ok {
		return 1
	}

	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")
	if !validateRunEnvVars(runID, eventLogPath, stderr) {
		return 1
	}

	turnID, ok := requireTurnID(getenv, stderr)
	if !ok {
		return 1
	}

	if !validateSubmitReviewInputs(flags.Verdict, flags.MergeConfidence, flags.Body, flags.PR, stderr) {
		return 1
	}

	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	defer writer.Close()

	existingEvents, err := readEventsForDedup(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: %v\n", err)
		return 1
	}
	if eventlog.HasTurnTypeEvent(existingEvents, turnID, eventlog.EventReviewSubmitted) {
		fmt.Fprintf(stdout, "review already submitted for this turn\n")
		return 0
	}

	owner, repoName, err := getRepoContext(executor)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: %v\n", err)
		return 1
	}

	reviewID, _, err := discoverOrCreatePendingReview(executor, owner, repoName, flags.PR)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: %v\n", err)
		return 1
	}

	return submitAndRecordReview(executor, writer, reviewID, flags.Verdict, flags.Body, flags.MergeConfidence, runID, flags.PR, turnID, stdout, stderr)
}

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
		fmt.Fprintln(stderr, "--issue must be a positive integer")
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get current directory: %v\n", err)
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
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	if _, err := credentials.NewLoader(homeDir).Load(cfg.Project); err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	if _, err := executor.RunInDir(repoRoot, "golemic", "preflight", "--check"); err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: preflight check: %v\n", err)
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
