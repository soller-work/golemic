package loop

const (
	StepPrepare     StepKey = "PREPARE"
	StepRunDev      StepKey = "RUN_DEV"
	StepSyncCI      StepKey = "SYNC_CI"
	StepRunReviewer StepKey = "RUN_REVIEWER"
	StepMergePR     StepKey = "MERGE_PR"

	StepTerminalSuccess      StepKey = "TERMINAL_SUCCESS"
	StepTerminalDevFailed    StepKey = "TERMINAL_DEV_FAILED"
	StepTerminalReviewFailed StepKey = "TERMINAL_REVIEW_FAILED"
	StepTerminalEscalated    StepKey = "TERMINAL_ESCALATED"
	StepTerminalMergeFailed  StepKey = "TERMINAL_MERGE_FAILED"
	StepTerminalTimeout      StepKey = "TERMINAL_TIMEOUT"
	StepTerminalStalled      StepKey = "TERMINAL_STALLED"
	StepTerminalAborted      StepKey = "TERMINAL_ABORTED"
	StepTerminalSkipped      StepKey = "TERMINAL_SKIPPED"
)

const (
	EventReady              EventKey = "READY"
	EventNotEligible        EventKey = "NOT_ELIGIBLE"
	EventPrepareFailed      EventKey = "PREPARE_FAILED"
	EventDevDone            EventKey = "DEV_DONE"
	EventDevGateRejected    EventKey = "DEV_GATE_REJECTED"
	EventDevFailed          EventKey = "DEV_FAILED"
	EventCIGreen            EventKey = "CI_GREEN"
	EventCIFailed           EventKey = "CI_FAILED"
	EventReviewApproved     EventKey = "REVIEW_APPROVED"
	EventChangesRequested   EventKey = "CHANGES_REQUESTED"
	EventPrecheckFailed     EventKey = "PRECHECK_FAILED"
	EventReviewFailed       EventKey = "REVIEW_FAILED"
	EventMerged             EventKey = "MERGED"
	EventMergeFailed        EventKey = "MERGE_FAILED"
	EventConflictResolved   EventKey = "CONFLICT_RESOLVED"
	EventConflictUnresolved EventKey = "CONFLICT_UNRESOLVED"
	EventAgentTimedOut      EventKey = "AGENT_TIMED_OUT"
	EventAgentStalled       EventKey = "AGENT_STALLED"
	EventAgentAborted       EventKey = "AGENT_ABORTED"
)

// AllSteps returns every declared StepKey. A new step must be added here.
func AllSteps() []StepKey {
	return []StepKey{
		StepPrepare, StepRunDev, StepSyncCI, StepRunReviewer, StepMergePR,
		StepTerminalSuccess, StepTerminalDevFailed, StepTerminalReviewFailed,
		StepTerminalEscalated, StepTerminalMergeFailed, StepTerminalTimeout,
		StepTerminalStalled, StepTerminalAborted, StepTerminalSkipped,
	}
}

// AllEvents returns every declared EventKey. A new event must be added here.
func AllEvents() []EventKey {
	return []EventKey{
		EventReady, EventNotEligible, EventPrepareFailed, EventDevDone,
		EventDevGateRejected, EventDevFailed, EventCIGreen, EventCIFailed,
		EventReviewApproved, EventChangesRequested, EventPrecheckFailed,
		EventReviewFailed, EventMerged, EventMergeFailed,
		EventConflictResolved, EventConflictUnresolved,
		EventAgentTimedOut, EventAgentStalled, EventAgentAborted,
	}
}
