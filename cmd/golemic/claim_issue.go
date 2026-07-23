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

// runClaimIssue implements `golemic claim-issue --number N`.
// Exit codes: 0 (ok/idempotent), 1 (env/config/gh error), 3 (race lost), 4 (not takeable).
func runClaimIssue(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int {
	fs := flag.NewFlagSet("claim-issue", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var numberFlag int
	fs.IntVar(&numberFlag, "number", 0, "Issue number to claim (required, positive integer)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	if numberFlag <= 0 {
		fmt.Fprintln(stderr, "--number must be a positive integer")
		return 1
	}

	runID, eventLogPath, turnID, ok := parseClaimEnvVars(getenv, stderr)
	if !ok {
		return 1
	}

	devLogin, devToken, err := claim.ResolveCredentials(executor)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err)
		return 1
	}

	result, claimErr := claim.Claim(executor, numberFlag, devLogin, devToken)

	switch result {
	case claim.ResultIdempotent:
		fmt.Fprintf(stdout, "already claimed issue #%d\n", numberFlag)
		return 0

	case claim.ResultNotTakeable:
		fmt.Fprintf(stderr, "issue #%d is not takeable\n", numberFlag)
		return 4

	case claim.ResultRaceLost:
		fmt.Fprintf(stderr, "claim conflict on issue #%d: %v\n", numberFlag, claimErr)
		return 3

	case claim.ResultOK:
		return writeIssueClaimedEvent(eventLogPath, runID, turnID, numberFlag, devLogin, stdout, stderr)

	case claim.ResultError:
		fmt.Fprintf(stderr, "gh claim failed: %v\n", claimErr)
		return 1
	}
	return 1
}

// parseClaimEnvVars reads and validates GOLEMIC_RUN_ID, GOLEMIC_EVENT_LOG, and
// GOLEMIC_TURN_ID from the environment. Returns the values and ok=true on success.
func parseClaimEnvVars(getenv func(string) string, stderr io.Writer) (runID, eventLogPath string, turnID int, ok bool) {
	runID = getenv("GOLEMIC_RUN_ID")
	eventLogPath = getenv("GOLEMIC_EVENT_LOG")
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
		fmt.Fprintf(stderr, "missing required environment variable: %s\n", strings.Join(missing, ", "))
		return "", "", 0, false
	}

	n, err := strconv.Atoi(turnIDStr)
	if err != nil || n < 0 {
		fmt.Fprintf(stderr, "missing required environment variable: GOLEMIC_TURN_ID\n")
		return "", "", 0, false
	}
	return runID, eventLogPath, n, true
}

// writeIssueClaimedEvent writes an issue_claimed event to the event log.
func writeIssueClaimedEvent(eventLogPath, runID string, turnID, numberFlag int, devLogin string, stdout, stderr io.Writer) int {
	payload, err := eventlog.MarshalIssueClaimedPayload(numberFlag, "ok")
	if err != nil {
		fmt.Fprintf(stderr, "failed to write event: %v\n", err)
		return 1
	}
	writer, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		fmt.Fprintf(stderr, "failed to write event: %v\n", err)
		return 1
	}
	defer writer.Close()

	ev := eventlog.Event{
		Type:    eventlog.EventIssueClaimed,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: payload,
	}
	if err := writer.Write(ev); err != nil {
		fmt.Fprintf(stderr, "failed to write event: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "claimed issue #%d as %s\n", numberFlag, devLogin)
	return 0
}
