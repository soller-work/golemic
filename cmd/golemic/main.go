package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/runner"
)

var knownCommands = []struct {
	name string
	desc string
}{
	{"preflight", "Check prerequisites"},
	{"run", "Run the main process (golemic run --issue N)"},
	{"emit", "Emit an event to the run log"},
	{"open-pr", "Open a pull request (not implemented)"},
	{"submit-review", "Submit a review (not implemented)"},
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

		// Try to find git root; fall back to cwd
		gitRoot, err := osExecutor{}.Run("git", "rev-parse", "--show-toplevel")
		repoRoot := cwd
		if err == nil && gitRoot != "" {
			repoRoot = strings.TrimSpace(gitRoot)
		}

		return runPreflight(osExecutor{}, homeDir, repoRoot, stdout, stderr)
	}

	if command == "run" {
		return runRun(args, stdout, stderr)
	}

	if command == "emit" {
		return runEmit(args, stdout, stderr, os.Getenv)
	}

	if command == "open-pr" {
		return runOpenPR(args, stdout, stderr, os.Getenv, osExecutor{})
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

// runPreflight executes the preflight command with injectable dependencies.
// All external effects (executor, homeDir, repoRoot) are parameters so tests
// can use fakes and temp directories.
func runPreflight(executor preflight.Executor, homeDir, repoRoot string, stdout, stderr io.Writer) int {

	p := preflight.New(executor, homeDir, repoRoot)
	p.SetStdout(stdout)

	results := p.RunAll()

	if results.AllOK() {
		fmt.Fprintln(stdout, "SUCCESS")
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

	// Parse flags from args[2:] (after "golemic emit")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	// BR-004: Check env vars before any I/O.
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
		fmt.Fprintf(stderr, "Missing required environment variable: %s\n", strings.Join(missing, ", "))
		return 1
	}

	// BR-001: --type must be non-empty.
	if typeFlag == "" {
		fmt.Fprintln(stderr, "--type must not be empty")
		return 1
	}

	// BR-002: --payload must be valid JSON that decodes to a JSON object.
	var payloadObj interface{}
	if err := json.Unmarshal([]byte(payloadFlag), &payloadObj); err != nil {
		fmt.Fprintf(stderr, "Invalid --payload: %v\n", err)
		return 1
	}

	// Verify it is a JSON object (not array, string, number, or null).
	payloadMap, isObject := payloadObj.(map[string]interface{})
	if !isObject {
		fmt.Fprintf(stderr, "Invalid --payload: JSON value must be an object, got %T\n", payloadObj)
		return 1
	}

	// Re-encode to normalise formatting.
	normalizedPayload, err := json.Marshal(payloadMap)
	if err != nil {
		fmt.Fprintf(stderr, "Invalid --payload: %v\n", err)
		return 1
	}

	// Create writer and append the event.
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
		Payload: normalizedPayload,
	}

	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}

	return 0
}

// runOpenPR executes the open-pr subcommand: golemic open-pr --title <t> --body <b>
// It validates env var context, resolves the current branch, creates a PR via gh,
// parses the PR number and URL, and writes a pr_opened event atomically.
func runOpenPR(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int {
	fs := flag.NewFlagSet("open-pr", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var titleFlag string
	var bodyFlag string
	fs.StringVar(&titleFlag, "title", "", "PR title (required)")
	fs.StringVar(&bodyFlag, "body", "", "PR body (required)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	// BR-004: Check env vars before any gh/git call.
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
		fmt.Fprintf(stderr, "Missing required environment variable: %s\n", strings.Join(missing, ", "))
		return 1
	}

	// Validate --title and --body must be non-empty (IF-001 constraints).
	if titleFlag == "" {
		fmt.Fprintln(stderr, "--title must not be empty")
		return 1
	}
	if bodyFlag == "" {
		fmt.Fprintln(stderr, "--body must not be empty")
		return 1
	}

	// BR-001: Get current branch via git branch --show-current.
	branchOut, err := executor.Run("git", "branch", "--show-current")
	if err != nil {
		fmt.Fprintf(stderr, "Failed to determine current branch: %v\n", err)
		return 1
	}
	branch := strings.TrimSpace(branchOut)
	if branch == "" {
		fmt.Fprintln(stderr, "Failed to determine current branch: detached HEAD or not on a branch")
		return 1
	}

	// BR-002, IC-001: Create PR via gh pr create.
	// GH_TOKEN is inherited from the process environment (BR-005).
	prOut, err := executor.RunWithEnv(
		nil, // no additional env vars; GH_TOKEN comes from process
		"gh", "pr", "create",
		"--title", titleFlag,
		"--body", bodyFlag,
		"--base", "main",
		"--head", branch,
	)
	if err != nil {
		var ee *preflight.ErrExit
		if errors.As(err, &ee) {
			fmt.Fprintf(stderr, "Failed to create PR: %s\n", strings.TrimSpace(ee.Stderr))
		} else {
			fmt.Fprintf(stderr, "Failed to create PR: %v\n", err)
		}
		return 1
	}

	// Parse PR number and URL from gh output.
	// gh pr create outputs the PR URL on stdout, e.g.:
	//   https://github.com/owner/repo/pull/123
	prURL := strings.TrimSpace(prOut)
	if prURL == "" {
		fmt.Fprintln(stderr, "Failed to parse PR number/URL from gh output: empty output")
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
		fmt.Fprintf(stderr, "Failed to parse PR number/URL from gh output: %s\n", prURL)
		return 1
	}

	// Write pr_opened event (SC-002). Event is written only after gh succeeds.
	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}
	defer writer.Close()

	payload := map[string]string{
		"prNumber": prNumber,
		"url":      prURL,
		"branch":   branch,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}

	event := eventlog.Event{
		Type:    eventlog.EventPROpened,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		Payload: payloadJSON,
	}

	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return 1
	}

	// Print PR URL to stdout for the caller.
	fmt.Fprintln(stdout, prURL)
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
	fs.IntVar(&issueNum, "issue", 0, "GitHub issue number (required)")

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

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}
