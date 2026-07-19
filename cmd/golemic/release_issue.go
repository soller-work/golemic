package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golemic/internal/claim"
	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
	"golemic/internal/repo"
)

func registerReleaseFlags(fs *flag.FlagSet) (*int, *string) {
	numberFlag := fs.Int("number", 0, "Issue number to release (required, positive integer)")
	reasonFlag := fs.String("reason", "", "Release reason: done|failed|abandoned (required)")
	return numberFlag, reasonFlag
}

func validateReleaseFlagsValues(numberFlag int, reasonFlag string, stderr io.Writer) int {
	if numberFlag <= 0 {
		fmt.Fprintln(stderr, "--number must be a positive integer") //nolint:errcheck
		return 1
	}
	switch reasonFlag {
	case "done", "failed", "abandoned":
	default:
		fmt.Fprintln(stderr, "invalid --reason: must be one of done|failed|abandoned") //nolint:errcheck
		return 1
	}
	return 0
}

func validateReleaseEnv(getenv func(string) string, stderr io.Writer) (string, string, int, int) {
	runID := getenv("GOLEMIC_RUN_ID")
	eventLogPath := getenv("GOLEMIC_EVENT_LOG")
	turnIDStr := getenv("GOLEMIC_TURN_ID")

	var missing []string
	if runID == "" {
		missing = append(missing, "GOLEMIC_RUN_ID")
	}
	if eventLogPath == "" {
		missing = append(missing, "GOLEMIC_EVENT_LOG")
	}
	if turnIDStr == "" {
		missing = append(missing, "GOLEMIC_TURN_ID")
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "missing required environment variable: %s\n", strings.Join(missing, ", ")) //nolint:errcheck
		return "", "", 0, 1
	}

	turnID, err := strconv.Atoi(turnIDStr)
	if err != nil || turnID < 0 {
		fmt.Fprintf(stderr, "missing required environment variable: GOLEMIC_TURN_ID\n") //nolint:errcheck
		return "", "", 0, 1
	}
	return runID, eventLogPath, turnID, 0
}

func resolveReleaseCredentials(executor preflight.Executor, stderr io.Writer) (string, int) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: failed to get home directory: %v\n", err) //nolint:errcheck
		return "", 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: failed to get working directory: %v\n", err) //nolint:errcheck
		return "", 1
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err) //nolint:errcheck
		return "", 1
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err) //nolint:errcheck
		return "", 1
	}

	creds, err := credentials.NewLoader(homeDir).Load(cfg.Project)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err) //nolint:errcheck
		return "", 1
	}

	return creds.DevToken(), 0
}

func lookupGHUser(executor preflight.Executor, devToken string, stderr io.Writer) (string, int) {
	userOut, err := executor.RunWithEnv(
		map[string]string{"GH_TOKEN": devToken},
		"gh", "api", "user",
	)
	if err != nil {
		fmt.Fprintf(stderr, "gh api user failed: %v\n", err) //nolint:errcheck
		return "", 1
	}
	var ghUser struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(userOut), &ghUser); err != nil {
		fmt.Fprintf(stderr, "gh api user: failed to parse response: %v\n", err) //nolint:errcheck
		return "", 1
	}
	if ghUser.Login == "" {
		fmt.Fprintln(stderr, "gh api user: login field is empty") //nolint:errcheck
		return "", 1
	}
	return ghUser.Login, 0
}

func writeReleaseEvent(eventLogPath, runID string, turnID, numberFlag int, reasonFlag string, stdout io.Writer, stderr io.Writer) int {
	payload, err := eventlog.MarshalIssueReleasedPayload(numberFlag, reasonFlag)
	if err != nil {
		fmt.Fprintf(stderr, "failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}
	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}
	defer writer.Close() //nolint:errcheck

	ev := eventlog.Event{
		Type:    eventlog.EventIssueReleased,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: payload,
	}
	if err := writer.Write(ev); err != nil {
		fmt.Fprintf(stderr, "failed to write event: %v\n", err) //nolint:errcheck
		return 1
	}
	fmt.Fprintf(stdout, "released issue #%d as %s\n", numberFlag, reasonFlag) //nolint:errcheck
	return 0
}

// runReleaseIssue implements `golemic release-issue --number N --reason done|failed|abandoned`.
// Exit codes: 0 (ok/idempotent), 1 (env/config/usage/gh error), 3 (foreign-owned lock).
func runReleaseIssue(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int {
	fs := flag.NewFlagSet("release-issue", flag.ContinueOnError)
	fs.SetOutput(stderr)

	numberFlag, reasonFlag := registerReleaseFlags(fs)

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	if code := validateReleaseFlagsValues(*numberFlag, *reasonFlag, stderr); code != 0 {
		return code
	}

	runID, eventLogPath, turnID, code := validateReleaseEnv(getenv, stderr)
	if code != 0 {
		return code
	}

	devToken, code := resolveReleaseCredentials(executor, stderr)
	if code != 0 {
		return code
	}

	ghLogin, code := lookupGHUser(executor, devToken, stderr)
	if code != 0 {
		return code
	}

	result, releaseErr := claim.Release(executor, *numberFlag, ghLogin, devToken, *reasonFlag)

	switch result {
	case claim.ReleaseResultIdempotent:
		fmt.Fprintf(stdout, "already released issue #%d\n", *numberFlag) //nolint:errcheck
		return 0
	case claim.ReleaseResultForeignClaim:
		fmt.Fprintf(stderr, "%v\n", releaseErr) //nolint:errcheck
		return 3
	case claim.ReleaseResultOK:
		return writeReleaseEvent(eventLogPath, runID, turnID, *numberFlag, *reasonFlag, stdout, stderr)
	case claim.ReleaseResultError:
		fmt.Fprintf(stderr, "gh release failed: %v\n", releaseErr) //nolint:errcheck
		return 1
	}
	return 1
}
