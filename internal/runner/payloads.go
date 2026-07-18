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

// issueLabel holds the name of a GitHub issue label.
type issueLabel struct {
	Name string `json:"name"`
}

// issueData holds the parsed result of gh issue view --json title,body,labels.
type issueData struct {
	Number int          `json:"number"`
	Title  string       `json:"title"`
	Body   string       `json:"body"`
	Labels []issueLabel `json:"labels"`
}
