package runner

import (
	"encoding/json"
	"fmt"

	"golemic/internal/eventlog"
)

const (
	outcomeSuccess      = "success"
	outcomeDevFailed    = "dev_failed"
	outcomeReviewFailed = "review_failed"
	outcomeEscalated    = "escalated"
	outcomeTimeout      = "timeout"
	outcomeStalled      = "stalled"
	outcomeAborted      = "aborted"
	outcomeMergeFailed  = "merge_failed"
	outcomeSkipped      = "skipped"
	branchPrefix        = "golemic/issue-"
)

// countReviewSubmittedEvents counts the number of review_submitted events in the log.
func (r *Runner) countReviewSubmittedEvents(eventLogPath string) int {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return 0
	}
	count := 0
	for _, ev := range events {
		if ev.Type == eventlog.EventReviewSubmitted {
			count++
		}
	}
	return count
}

// latestReviewBody reads the body field from the most recent review_submitted event.
func (r *Runner) latestReviewBody(eventLogPath string) (string, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			var d struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(events[i].Payload, &d); err != nil {
				return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
			}
			return d.Body, nil
		}
	}
	return "", fmt.Errorf("NO_VALID_REVIEW: no review_submitted event found")
}

// hasPROpenedEvent checks if a valid pr_opened event exists in the log.
func (r *Runner) hasPROpenedEvent(eventLogPath string) bool {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return false
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventPROpened {
			if err := eventlog.ValidatePROpenedPayload(events[i].Payload); err != nil {
				return false
			}
			return true
		}
	}
	return false
}

// latestMergeConfidence reads the mergeConfidence field from the most recent review_submitted event.
func (r *Runner) latestMergeConfidence(eventLogPath string) (string, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			var d struct {
				MergeConfidence string `json:"mergeConfidence"`
			}
			if err := json.Unmarshal(events[i].Payload, &d); err != nil {
				return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
			}
			return d.MergeConfidence, nil
		}
	}
	return "", fmt.Errorf("NO_VALID_REVIEW: no review_submitted event found")
}

// latestReviewVerdict reads the verdict from the most recent review_submitted event.
// Returns the verdict string ("approved" or "changes_requested") or an error if
// no valid review_submitted event exists.
func (r *Runner) latestReviewVerdict(eventLogPath string) (string, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			if err := eventlog.ValidateReviewSubmittedPayload(events[i].Payload); err != nil {
				return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
			}
			var d struct {
				Verdict string `json:"verdict"`
			}
			if err := json.Unmarshal(events[i].Payload, &d); err != nil {
				return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
			}
			return d.Verdict, nil
		}
	}
	return "", fmt.Errorf("NO_VALID_REVIEW: no review_submitted event found")
}
