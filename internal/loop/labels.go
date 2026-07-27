package loop

var stepLabels = map[StepKey]string{
	StepPrepare:              "preparing",
	StepRunDev:               "running dev agent",
	StepSyncCI:               "waiting for CI",
	StepRunReviewer:          "running reviewer",
	StepMergePR:              "merging PR",
	StepTerminalSuccess:      "success",
	StepTerminalDevFailed:    "dev failed",
	StepTerminalReviewFailed: "review failed",
	StepTerminalEscalated:    "escalated",
	StepTerminalMergeFailed:  "merge failed",
	StepTerminalTimeout:      "timed out",
	StepTerminalStalled:      "stalled",
	StepTerminalAborted:      "aborted",
	StepTerminalSkipped:      "skipped",
}

var eventLabels = map[EventKey]string{
	EventReady:            "ready",
	EventNotEligible:      "not eligible",
	EventPrepareFailed:    "prepare failed",
	EventDevDone:          "dev done",
	EventDevGateRejected:  "gate rejected",
	EventDevFailed:        "dev failed",
	EventCIGreen:          "CI green",
	EventCIFailed:         "CI failed",
	EventReviewApproved:   "review approved",
	EventChangesRequested: "changes requested",
	EventPrecheckFailed:   "precheck failed",
	EventReviewFailed:     "review failed",
	EventMerged:           "merged",
	EventMergeFailed:      "merge failed",
	EventAgentTimedOut:    "agent timed out",
	EventAgentStalled:     "agent stalled",
	EventAgentAborted:     "agent aborted",
}

var terminalSteps = map[StepKey]bool{
	StepTerminalSuccess:      true,
	StepTerminalDevFailed:    true,
	StepTerminalReviewFailed: true,
	StepTerminalEscalated:    true,
	StepTerminalMergeFailed:  true,
	StepTerminalTimeout:      true,
	StepTerminalStalled:      true,
	StepTerminalAborted:      true,
	StepTerminalSkipped:      true,
}

var successTerminals = map[StepKey]bool{
	StepTerminalSuccess: true,
	StepTerminalSkipped: true,
}

// StepLabel returns the human-readable label for k and whether one exists.
// Falls back to the raw key string when the label is missing.
func StepLabel(k StepKey) (string, bool) {
	label, ok := stepLabels[k]
	return label, ok
}

// EventLabel returns the human-readable label for k and whether one exists.
func EventLabel(k EventKey) (string, bool) {
	label, ok := eventLabels[k]
	return label, ok
}

// IsTerminalStep reports whether k is a terminal step.
func IsTerminalStep(k StepKey) bool {
	return terminalSteps[k]
}

// IsSuccessTerminal reports whether k is a terminal step that indicates success.
func IsSuccessTerminal(k StepKey) bool {
	return successTerminals[k]
}
