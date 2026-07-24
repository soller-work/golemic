package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golemic/internal/claim"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
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

	ghLogin, devToken, err := claim.ResolveCredentials(executor)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err) //nolint:errcheck
		return 1
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
