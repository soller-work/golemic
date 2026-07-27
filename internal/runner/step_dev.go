package runner

import (
	"fmt"
	"path/filepath"

	"golemic/internal/loop"
	"golemic/internal/prompt"
)

// stepRunDev runs exactly ONE dev-agent invocation based on ctx.DevMode and
// ctx.DevAttempt, then classifies the result as a loop event.
//
//	ctx.DevAttempt == 0            → initial prompt (RenderDev) or retry prompt
//	                                 (RenderDevRetry) depending on ctx.DevMode
//	ctx.DevAttempt > 0             → gate-retry prompt (RenderDevGateRetry) with ctx.GateReason
//
// Event mapping (exhaustive):
//
//	gate passed, side effects done → resets ctx.DevAttempt = 0, returns loop.EventDevDone
//	§10 gate rejected              → sets ctx.GateReason, ctx.DevAttempt++, returns loop.EventDevGateRejected
//	agent timeout                  → loop.EventAgentTimedOut
//	agent stalled                  → loop.EventAgentStalled
//	agent thinking-loop            → loop.EventAgentAborted
//	anything else                  → loop.EventDevFailed
func (r *Runner) stepRunDev(ctx *RunContext) loop.EventKey {
	systemPromptFile, model, cleanupPrompt, err := r.resolveAgentFile("dev")
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err) //nolint:errcheck
		return loop.EventDevFailed
	}
	defer cleanupPrompt()

	// CBM indexing only on attempt 0 to avoid expensive re-indexing on gate retries (BR-2).
	cbmEnabled := r.cfg.CodebaseMemory.Enabled && ctx.DevAttempt == 0
	if cbmEnabled {
		devWorktreePath := filepath.Join(ctx.GolemicDir, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
		cbmCacheDir := filepath.Join(ctx.GolemicDir, "cbm", fmt.Sprintf("issue-%d", r.issueNum))
		projectName := fmt.Sprintf("golemic-issue-%d-dev", r.issueNum)
		cbmEnabled = r.indexWorktree(devWorktreePath, cbmCacheDir, projectName)
	}

	userPrompt, err := r.renderDevPrompt(ctx, cbmEnabled)
	if err != nil {
		fmt.Fprintf(r.stderr, "dev_failed: %v\n", err) //nolint:errcheck
		return loop.EventDevFailed
	}

	ev, gateReason := r.runDevAgentWithPrompt(
		ctx.GolemicDir, ctx.EventLogPath,
		systemPromptFile, model, userPrompt, cbmEnabled,
		ctx.Timeout, ctx.ParentSpanID,
		ctx.Round, ctx.DevAttempt,
	)

	switch ev {
	case loop.EventDevDone:
		ctx.DevAttempt = 0
		return loop.EventDevDone
	case loop.EventDevGateRejected:
		ctx.GateReason = gateReason
		ctx.DevAttempt++
		return loop.EventDevGateRejected
	default:
		return ev
	}
}

// renderDevPrompt selects and renders the correct user prompt based on DevMode and DevAttempt.
func (r *Runner) renderDevPrompt(ctx *RunContext, cbmEnabled bool) (string, error) {
	guidelinesPath := filepath.Join(r.repoRoot, ".golemic", "guidelines", "dev.md")
	issue := prompt.Issue{Number: r.issue.Number, Title: r.issue.Title}

	if ctx.DevAttempt > 0 {
		return prompt.RenderDevGateRetry(ctx.GateReason, issue, r.branchName, r.cfg.VerifyCommand, guidelinesPath)
	}
	if ctx.DevMode == DevModeRetryWithFindings {
		return prompt.RenderDevRetry(ctx.Findings, ctx.FindingsJSON, issue, r.branchName, r.cfg.VerifyCommand, guidelinesPath, cbmEnabled)
	}
	return prompt.RenderDev(issue, r.branchName, r.cfg.VerifyCommand, guidelinesPath, cbmEnabled)
}

// runDevTurn drives stepRunDev through the bounded gate-retry budget (3
// invocations) and converts the final event back to a legacy outcome string.
// This adapter is deleted in Slice 4 when the machine owns the self-loop.
func (r *Runner) runDevTurn(ctx *RunContext, mode DevMode) string {
	ctx.DevMode, ctx.DevAttempt = mode, 0
	for {
		ev := r.stepRunDev(ctx)
		switch ev {
		case loop.EventDevDone:
			return outcomeSuccess
		case loop.EventDevGateRejected:
			if ctx.DevAttempt >= 3 {
				fmt.Fprintf(r.stderr, "dev_failed: dev did not complete gm_dev_done after 3 invocations: %s\n", ctx.GateReason) //nolint:errcheck
				return outcomeDevFailed
			}
		case loop.EventAgentTimedOut:
			return outcomeTimeout
		case loop.EventAgentStalled:
			return outcomeStalled
		case loop.EventAgentAborted:
			return outcomeAborted
		default:
			return outcomeDevFailed
		}
	}
}
