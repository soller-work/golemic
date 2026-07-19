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

const (
	exitClaimRaceLost    = 3
	exitClaimNotTakeable = 4
)

// runClaimIssue implements `golemic claim-issue --number N`.
func runClaimIssue(args []string, stdout, stderr io.Writer, getenv func(string) string, executor preflight.Executor) int { //nolint:cyclop,funlen
	fs := flag.NewFlagSet("claim-issue", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var numberFlag int
	fs.IntVar(&numberFlag, "number", 0, "Issue number to claim (required)")

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
	if err != nil {
		fmt.Fprintf(stderr, "missing required environment variable: GOLEMIC_TURN_ID\n") //nolint:errcheck
		return 1
	}

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

	result, err := claim.Claim(executor, numberFlag, creds.DevToken())
	if err != nil {
		fmt.Fprintf(stderr, "gh call failed: %v\n", err) //nolint:errcheck
		return 1
	}

	switch result.Outcome {
	case claim.OutcomeOK:
		writer, err := eventlog.NewWriter(eventLogPath)
		if err != nil {
			fmt.Fprintf(stderr, "failed to write event: %v\n", err) //nolint:errcheck
			return 1
		}
		defer writer.Close() //nolint:errcheck

		payload, err := json.Marshal(map[string]interface{}{
			"issue_number":  numberFlag,
			"verify_result": "ok",
		})
		if err != nil {
			fmt.Fprintf(stderr, "failed to write event: %v\n", err) //nolint:errcheck
			return 1
		}

		event := eventlog.Event{
			Type:    eventlog.EventIssueClaimed,
			Ts:      time.Now().Format(time.RFC3339),
			RunID:   runID,
			TurnID:  turnID,
			Payload: payload,
		}
		if err := writer.Write(event); err != nil {
			fmt.Fprintf(stderr, "failed to write event: %v\n", err) //nolint:errcheck
			return 1
		}

		fmt.Fprintf(stdout, "claimed issue #%d as dev-bot\n", numberFlag) //nolint:errcheck
		return 0

	case claim.OutcomeIdempotent:
		fmt.Fprintf(stdout, "already claimed issue #%d\n", numberFlag) //nolint:errcheck
		return 0

	case claim.OutcomeRaceLost:
		fmt.Fprintf(stderr, "%s\n", result.Details) //nolint:errcheck
		return exitClaimRaceLost

	case claim.OutcomeNotTakeable:
		fmt.Fprintf(stderr, "%s\n", result.Details) //nolint:errcheck
		return exitClaimNotTakeable

	default:
		fmt.Fprintln(stderr, "unknown claim outcome") //nolint:errcheck
		return 1
	}
}
