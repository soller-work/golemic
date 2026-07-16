package runner

import "golemic/internal/eventlog"

const (
	outcomeSuccess      = "success"
	outcomeDevFailed    = "dev_failed"
	outcomeReviewFailed = "review_failed"
	outcomeTimeout      = "timeout"
	outcomeAborted      = "aborted"
	branchPrefix        = "golemic/issue-"
)

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

// hasReviewSubmittedEvent checks if a valid review_submitted event exists in the log.
func (r *Runner) hasReviewSubmittedEvent(eventLogPath string) bool {
	reader := eventlog.Reader{}
	events, err := reader.Read(eventLogPath)
	if err != nil {
		return false
	}

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventlog.EventReviewSubmitted {
			if err := eventlog.ValidateReviewSubmittedPayload(events[i].Payload); err != nil {
				return false
			}
			return true
		}
	}
	return false
}
