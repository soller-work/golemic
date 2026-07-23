package runner

import (
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/progress"
)

// followActivity starts the activity.jsonl follow reader if the renderer is set.
// Returns a no-op stop function when renderer is nil.
func followActivity(renderer *progress.Renderer, role, path string) func() {
	if renderer == nil {
		return func() {}
	}
	return progress.FollowActivityJSONL(role, path, renderer)
}

// agentWrittenTypes are event types written by agent subprocesses (not the runner).
// emitAgentWrittenEvents filters to only these when scanning events.jsonl.
var agentWrittenTypes = map[string]bool{
	eventlog.EventPROpened:        true,
	eventlog.EventReviewSubmitted: true,
	eventlog.EventIssueClaimed:    true,
	eventlog.EventIssueReleased:   true,
}

// progressEventWriter wraps an EventWriter and emits a lifecycle progress line
// via the renderer after each successful Write.
type progressEventWriter struct {
	inner    eventWriter
	renderer *progress.Renderer
}

// eventWriter is the minimal interface for writing events. Matches eventlog.Writer.
type eventWriter interface {
	Write(event eventlog.Event) error
}

func (w *progressEventWriter) Write(event eventlog.Event) error {
	err := w.inner.Write(event)
	if err == nil {
		w.renderer.EmitLifecycle(event)
	}
	return err
}

// emitAgentWrittenEvents reads events.jsonl from r.progressScanIndex onward,
// emits progress lines for agent-written event types, and advances the index.
// Non-fatal: errors in reading are silently ignored per BR-P3.
func (r *Runner) emitAgentWrittenEvents(eventLogPath string) {
	if r.progressRenderer == nil {
		return
	}
	events, err := eventlog.Reader{}.Read(eventLogPath)
	if err != nil {
		return
	}
	for i := r.progressScanIndex; i < len(events); i++ {
		if agentWrittenTypes[events[i].Type] {
			r.progressRenderer.EmitLifecycle(events[i])
		}
	}
	r.progressScanIndex = len(events)
}

// writeDevStarted appends a dev_started event to events.jsonl and emits a
// progress line. Errors are silently dropped (non-fatal per BR-P3).
func (r *Runner) writeDevStarted(eventLogPath string) {
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		return
	}
	defer w.Close() //nolint:errcheck

	ev := eventlog.Event{
		Type:   eventlog.EventDevStarted,
		Ts:     time.Now().Format(time.RFC3339),
		RunID:  r.runID,
		TurnID: r.turnCounter,
	}
	if writeErr := w.Write(ev); writeErr != nil {
		return
	}
	if r.progressRenderer != nil {
		r.progressRenderer.EmitLifecycle(ev)
	}
}
