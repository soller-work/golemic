package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// validateRunEnvVars checks that GOLEMIC_RUN_ID and GOLEMIC_EVENT_LOG are both
// non-empty. Returns false and writes a diagnostic to stderr on failure.
func validateRunEnvVars(runID, eventLogPath string, stderr io.Writer) bool {
	if runID != "" && eventLogPath != "" {
		return true
	}
	var missing []string
	if runID == "" {
		missing = append(missing, "GOLEMIC_RUN_ID")
	}
	if eventLogPath == "" {
		missing = append(missing, "GOLEMIC_EVENT_LOG")
	}
	fmt.Fprintf(stderr, "Missing required environment variable: %s\n", strings.Join(missing, ", "))
	return false
}

// parseAndNormalizePayload validates that payloadFlag is a non-empty JSON object
// and returns the re-encoded (normalised) bytes.
func parseAndNormalizePayload(typeFlag, payloadFlag string, stderr io.Writer) (json.RawMessage, bool) {
	if typeFlag == "" {
		fmt.Fprintln(stderr, "--type must not be empty")
		return nil, false
	}
	var payloadObj interface{}
	if err := json.Unmarshal([]byte(payloadFlag), &payloadObj); err != nil {
		fmt.Fprintf(stderr, "Invalid --payload: %v\n", err)
		return nil, false
	}
	payloadMap, isObject := payloadObj.(map[string]interface{})
	if !isObject {
		fmt.Fprintf(stderr, "Invalid --payload: JSON value must be an object, got %T\n", payloadObj)
		return nil, false
	}
	normalized, err := json.Marshal(payloadMap)
	if err != nil {
		fmt.Fprintf(stderr, "Invalid --payload: %v\n", err)
		return nil, false
	}
	return normalized, true
}

// formatGHError formats a preflight.ErrExit as its stderr text; for other errors
// it returns err.Error(). Used for consistent gh CLI error messages.
func formatGHError(err error) string {
	var ee *preflight.ErrExit
	if errors.As(err, &ee) {
		return strings.TrimSpace(ee.Stderr)
	}
	return err.Error()
}

// validateReviewCommentInputs validates all required flags for the review-comment command.
func validateReviewCommentInputs(prFlag, lineFlag int, pathFlag, sideFlag, bodyFlag string, startLineFlag int, stderr io.Writer) bool {
	if prFlag <= 0 {
		fmt.Fprintln(stderr, "--pr must be a positive integer")
		return false
	}
	if pathFlag == "" {
		fmt.Fprintln(stderr, "--path must not be empty")
		return false
	}
	if lineFlag <= 0 {
		fmt.Fprintln(stderr, "--line must be a positive integer")
		return false
	}
	if bodyFlag == "" {
		fmt.Fprintln(stderr, "--body must not be empty")
		return false
	}
	if sideFlag != "RIGHT" && sideFlag != "LEFT" {
		fmt.Fprintf(stderr, "--side must be RIGHT or LEFT, got %q\n", sideFlag)
		return false
	}
	if startLineFlag < 0 {
		fmt.Fprintln(stderr, "--start-line must be >= 0")
		return false
	}
	return true
}

// validateSubmitReviewInputs validates verdict, mergeConfidence, body, and pr flags.
func validateSubmitReviewInputs(verdictFlag, mergeConfidenceFlag, bodyFlag string, prFlag int, stderr io.Writer) bool {
	if mergeConfidenceFlag != "high" && mergeConfidenceFlag != "low" {
		fmt.Fprintf(stderr, "Invalid merge confidence: must be 'high' or 'low', got %q\n", mergeConfidenceFlag)
		return false
	}
	if verdictFlag != "approved" && verdictFlag != "changes_requested" {
		fmt.Fprintf(stderr, "Invalid verdict: must be 'approved' or 'changes_requested', got %q\n", verdictFlag)
		return false
	}
	if bodyFlag == "" {
		fmt.Fprintln(stderr, "--body must not be empty")
		return false
	}
	if prFlag <= 0 {
		fmt.Fprintln(stderr, "--pr must be a positive integer")
		return false
	}
	return true
}

// parsePRNumberFromURL extracts the PR number from a gh pr create URL.
func parsePRNumberFromURL(prURL string, stderr io.Writer) (string, bool) {
	if prURL == "" {
		fmt.Fprintln(stderr, "Failed to parse PR number/URL from gh output: empty output")
		return "", false
	}
	var prNumber string
	if idx := strings.LastIndex(prURL, "/"); idx >= 0 {
		candidate := prURL[idx+1:]
		if _, convErr := strconv.Atoi(candidate); convErr == nil {
			prNumber = candidate
		}
	}
	if prNumber == "" {
		fmt.Fprintf(stderr, "Failed to parse PR number/URL from gh output: %s\n", prURL)
		return "", false
	}
	return prNumber, true
}

// recordPROpenedEvent writes a pr_opened event to the event log.
func recordPROpenedEvent(writer *eventlog.Writer, setup *openPRSetup, prNumber, prURL string, stderr io.Writer) bool {
	payload := map[string]string{
		"prNumber": prNumber,
		"url":      prURL,
		"branch":   setup.branch,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return false
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
		return false
	}
	return true
}

// parseSubmitReviewResponse parses the GraphQL response and extracts review ID and comment count.
func parseSubmitReviewResponse(submitOut string, stderr io.Writer) (string, int, bool) {
	var submitResp struct {
		Data struct {
			SubmitPullRequestReview struct {
				PullRequestReview struct {
					FullDatabaseID string `json:"fullDatabaseId"`
					Comments       struct {
						TotalCount int `json:"totalCount"`
					} `json:"comments"`
				} `json:"pullRequestReview"`
			} `json:"submitPullRequestReview"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(submitOut), &submitResp); err != nil {
		fmt.Fprintf(stderr, "Failed to submit review: failed to parse response: %v\n", err)
		return "", 0, false
	}
	submittedReviewID := submitResp.Data.SubmitPullRequestReview.PullRequestReview.FullDatabaseID
	if submittedReviewID == "" {
		fmt.Fprintf(stderr, "Failed to submit review: response missing review id\n")
		return "", 0, false
	}
	inlineCount := submitResp.Data.SubmitPullRequestReview.PullRequestReview.Comments.TotalCount
	return submittedReviewID, inlineCount, true
}

// recordReviewSubmittedEvent writes a review_submitted event to the event log.
func recordReviewSubmittedEvent(writer *eventlog.Writer, verdictFlag, bodyFlag, mergeConfidenceFlag, submittedReviewID, runID string, prFlag, turnID, inlineCount int, stderr io.Writer) bool {
	payload := map[string]interface{}{
		"verdict":            verdictFlag,
		"body":               bodyFlag,
		"prNumber":           prFlag,
		"mergeConfidence":    mergeConfidenceFlag,
		"reviewId":           submittedReviewID,
		"inlineCommentCount": inlineCount,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return false
	}
	event := eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   runID,
		TurnID:  turnID,
		Payload: payloadJSON,
	}
	if err := writer.Write(event); err != nil {
		fmt.Fprintf(stderr, "Failed to write event: %v\n", err)
		return false
	}
	return true
}

// setMergeConfidenceLabel sets the merge confidence label on the PR.
func setMergeConfidenceLabel(executor preflight.Executor, mergeConfidenceFlag string, prFlag int, stderr io.Writer) bool {
	confidenceLabel := "confidence:" + mergeConfidenceFlag
	_, _ = executor.RunWithEnv(nil, "gh", "label", "create", confidenceLabel, "--color", "0075ca", "--description", "merge confidence")
	if _, err := executor.RunWithEnv(nil, "gh", "pr", "edit", strconv.Itoa(prFlag), "--add-label", confidenceLabel); err != nil {
		fmt.Fprintf(stderr, "Review submitted but PR label could not be set: %v\n", err)
		return false
	}
	return true
}

// parseSubmitReviewFlags parses command-line flags for the submit-review subcommand.
type submitReviewFlags struct {
	Verdict         string
	Body            string
	PR              int
	MergeConfidence string
}

func parseSubmitReviewFlags(args []string, stderr io.Writer) (submitReviewFlags, bool) {
	fs := flag.NewFlagSet("submit-review", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var flags submitReviewFlags
	fs.StringVar(&flags.Verdict, "verdict", "", "Verdict: 'approved' or 'changes_requested' (required)")
	fs.StringVar(&flags.Body, "body", "", "Review body (required)")
	fs.IntVar(&flags.PR, "pr", 0, "PR number (required)")
	fs.StringVar(&flags.MergeConfidence, "merge-confidence", "", "Merge confidence: 'high' or 'low' (required)")

	if err := fs.Parse(args[2:]); err != nil {
		return submitReviewFlags{}, false
	}
	return flags, true
}
