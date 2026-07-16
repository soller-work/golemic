package runner

// runStartedPayload is the payload for run_started events.
type runStartedPayload struct {
	Issue int    `json:"issue"`
	RunID string `json:"runId"`
}

// runFinishedPayload is the payload for run_finished events.
type runFinishedPayload struct {
	Outcome string `json:"outcome"`
}

// issueData holds the parsed result of gh issue view --json title,body.
type issueData struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}
