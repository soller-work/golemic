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

// latestReviewID reads the reviewId field from the most recent review_submitted event.
func (r *Runner) latestReviewID(eventLogPath string) (string, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			var d struct {
				ReviewID string `json:"reviewId"`
			}
			if err := json.Unmarshal(events[i].Payload, &d); err != nil {
				return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
			}
			if d.ReviewID == "" {
				return "", fmt.Errorf("NO_VALID_REVIEW: reviewId is empty in review_submitted event")
			}
			return d.ReviewID, nil
		}
	}
	return "", fmt.Errorf("NO_VALID_REVIEW: no review_submitted event found")
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

// latestConfidence reads the confidence field from the most recent review_submitted event.
func (r *Runner) latestConfidence(eventLogPath string) (string, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			var d struct {
				Confidence string `json:"confidence"`
			}
			if err := json.Unmarshal(events[i].Payload, &d); err != nil {
				return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
			}
			return d.Confidence, nil
		}
	}
	return "", fmt.Errorf("NO_VALID_REVIEW: no review_submitted event found")
}

// reviewEventMatchesCriteria returns false when the event payload does not match
// the given round or headSHA filter (non-zero/non-empty only).
func reviewEventMatchesCriteria(payload []byte, round int, headSHA string) bool {
	var meta struct {
		ReviewRound int    `json:"reviewRound"`
		HeadSHA     string `json:"headSha"`
	}
	if err := json.Unmarshal(payload, &meta); err != nil {
		return false
	}
	if round > 0 && meta.ReviewRound != round {
		return false
	}
	if headSHA != "" && meta.HeadSHA != headSHA {
		return false
	}
	return true
}

// latestReviewVerdict returns the verdict from the most recent review_submitted event
// that matches the given round and headSHA. When round==0 and headSHA=="", no
// filtering is applied (used by unit tests that write events without these fields).
func (r *Runner) latestReviewVerdict(eventLogPath string, round int, headSHA string) (string, error) {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
	}
	filter := round > 0 || headSHA != ""
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != eventlog.EventReviewSubmitted {
			continue
		}
		if err := eventlog.ValidateReviewSubmittedPayload(events[i].Payload); err != nil {
			return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
		}
		if filter && !reviewEventMatchesCriteria(events[i].Payload, round, headSHA) {
			continue
		}
		var d struct {
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(events[i].Payload, &d); err != nil {
			return "", fmt.Errorf("NO_VALID_REVIEW: %w", err)
		}
		return d.Verdict, nil
	}
	if filter {
		return "", fmt.Errorf("NO_VALID_REVIEW: no review_submitted event found for round %d head %s", round, headSHA)
	}
	return "", fmt.Errorf("NO_VALID_REVIEW: no review_submitted event found")
}
