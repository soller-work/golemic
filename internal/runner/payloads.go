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

// issueData holds issue metadata used by the runner (title + labels + state).
// The body is not stored here because the agents fetch it on demand via
// `gm_slice_get`.
type issueData struct {
	Number int          `json:"number"`
	Title  string       `json:"title"`
	Labels []issueLabel `json:"labels"`
	State  string       `json:"state"`
}
