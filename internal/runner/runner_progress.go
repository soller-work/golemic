package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"golemic/internal/agent"
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

// emitAgentContext persists the full user prompt to disk and emits a framed
// context block on the progress stream. Non-fatal: errors are ignored.
func (r *Runner) emitAgentContext(cfg agent.RoleConfig) {
	if r.progressRenderer == nil {
		return
	}
	promptFile := filepath.Join(cfg.RunsDir, cfg.RunID,
		fmt.Sprintf("%s-r%d-a%d.prompt.md", cfg.Role, cfg.Round, cfg.Attempt))
	// Write prompt to disk (non-fatal).
	if err := os.MkdirAll(filepath.Dir(promptFile), 0o755); err == nil {
		_ = os.WriteFile(promptFile, []byte(cfg.UserPrompt), 0o644)
	}
	r.progressRenderer.EmitAgentContext(progress.AgentContextParams{
		Role:       cfg.Role,
		Model:      cfg.Model,
		Round:      cfg.Round,
		Attempt:    cfg.Attempt,
		UserPrompt: cfg.UserPrompt,
		PromptFile: promptFile,
		Verbose:    r.verbose,
	})
}

// emitAgentWrittenEvents reads events.jsonl from r.progressScanIndex onward,
// emits progress lines for agent-written event types, and advances the index.
// Non-fatal: errors in reading are silently ignored.
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
