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

// runClaimIssue implements `golemic claim-issue --number N`.
// Exit codes: 0 (ok/idempotent), 1 (env/config/gh error), 3 (race lost), 4 (not takeable).
func runClaimIssue(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int { //nolint:cyclop,funlen,gocognit
	fs := flag.NewFlagSet("claim-issue", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var numberFlag int
	fs.IntVar(&numberFlag, "number", 0, "Issue number to claim (required, positive integer)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	if numberFlag <= 0 {
		fmt.Fprintln(stderr, "--number must be a positive integer") //nolint:errcheck
		return 1
	}

	// BR-004: fail-fast env var validation before any gh call.
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
		return 1
	}

	turnID, err := strconv.Atoi(turnIDStr)
	if err != nil || turnID < 0 {
		fmt.Fprintf(stderr, "missing required environment variable: GOLEMIC_TURN_ID\n") //nolint:errcheck
		return 1
	}

	// Resolve config and credentials.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: failed to get home directory: %v\n", err) //nolint:errcheck
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: failed to get working directory: %v\n", err) //nolint:errcheck
		return 1
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err) //nolint:errcheck
		return 1
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err) //nolint:errcheck
		return 1
	}

	creds, err := credentials.NewLoader(homeDir).Load(cfg.Project)
	if err != nil {
		fmt.Fprintf(stderr, "config/credentials error: %v\n", err) //nolint:errcheck
		return 1
	}

	devToken := creds.DevToken()

	// Resolve dev-bot GitHub login via gh api user (BR-005).
	userOut, err := executor.RunWithEnv(
		map[string]string{"GH_TOKEN": devToken},
		"gh", "api", "user",
	)
	if err != nil {
		fmt.Fprintf(stderr, "gh api user failed: %v\n", err) //nolint:errcheck
		return 1
	}
	var ghUser struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(userOut), &ghUser); err != nil {
		fmt.Fprintf(stderr, "gh api user: failed to parse response: %v\n", err) //nolint:errcheck
		return 1
	}
	if ghUser.Login == "" {
		fmt.Fprintln(stderr, "gh api user: login field is empty") //nolint:errcheck
		return 1
	}

	result, claimErr := claim.Claim(executor, numberFlag, ghUser.Login, devToken)

	switch result {
	case claim.ResultIdempotent:
		fmt.Fprintf(stdout, "already claimed issue #%d\n", numberFlag) //nolint:errcheck
		return 0

	case claim.ResultNotTakeable:
		fmt.Fprintf(stderr, "issue #%d is not takeable\n", numberFlag) //nolint:errcheck
		return 4

	case claim.ResultRaceLost:
		fmt.Fprintf(stderr, "claim conflict on issue #%d: %v\n", numberFlag, claimErr) //nolint:errcheck
		return 3

	case claim.ResultOK:
		// write issue_claimed event (SC-003).
		payload, err := eventlog.MarshalIssueClaimedPayload(numberFlag, "ok")
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
			Type:    eventlog.EventIssueClaimed,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   runID,
			TurnID:  turnID,
			Payload: payload,
		}
		if err := writer.Write(ev); err != nil {
			fmt.Fprintf(stderr, "failed to write event: %v\n", err) //nolint:errcheck
			return 1
		}
		fmt.Fprintf(stdout, "claimed issue #%d as %s\n", numberFlag, ghUser.Login) //nolint:errcheck
		return 0

	default:
		// claimErr is non-nil for gh/parse failures.
		fmt.Fprintf(stderr, "gh claim failed: %v\n", claimErr) //nolint:errcheck
		return 1
	}
}
