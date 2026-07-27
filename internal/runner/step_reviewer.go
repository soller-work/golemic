package runner

import (
	"fmt"
	"path/filepath"

	"golemic/internal/loop"
	"golemic/internal/worktree"
)

// reviewerRound holds per-round values computed before the attempt loop.
type reviewerRound struct {
	prNumber     int
	roundHeadSHA string
	currentRound int
	countBefore  int
}

// prepareReviewerRound resolves the PR number, head SHA, and round index.
func (r *Runner) prepareReviewerRound(eventLogPath string) (reviewerRound, error) {
	prNumber, err := r.getPRNumber(eventLogPath)
	if err != nil {
		return reviewerRound{}, fmt.Errorf("get PR number: %w", err)
	}
	roundHeadSHA, err := r.getPRHeadSHA(prNumber)
	if err != nil {
		return reviewerRound{}, fmt.Errorf("get PR head SHA: %w", err)
	}
	countBefore := r.countReviewSubmittedEvents(eventLogPath)
	return reviewerRound{
		prNumber:     prNumber,
		roundHeadSHA: roundHeadSHA,
		currentRound: countBefore + 1,
		countBefore:  countBefore,
	}, nil
}

// stepRunReviewer runs ONE reviewer round and classifies it as a loop event.
// Sequence:
//  1. prepareReviewerWorktree
//  2. record roundHeadSHA and countBefore
//  3. attempts loop (max 3): sweep, precheck, agent
//  4. precheck !ok: synthetic event, escalate or set up findings
//  5. freshness gate
//  6. submitReviewAndWriteEvent + dirty-worktree check
//  7. update ctx.Round = countReviewSubmittedEvents
//  8. classify verdict as loop event
func (r *Runner) stepRunReviewer(ctx *RunContext) loop.EventKey {
	reviewerWT, failOutcome := r.prepareReviewerWorktree(ctx.GolemicDir, ctx.Writer, ctx.ParentSpanID, ctx.ReviewerWorktreeExists)
	if failOutcome != "" {
		return loop.EventReviewFailed
	}
	ctx.ReviewerWorktreeExists = true

	rnd, err := r.prepareReviewerRound(ctx.EventLogPath)
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: %v\n", err) //nolint:errcheck
		return loop.EventReviewFailed
	}

	finalState, failEvent := r.runReviewerAttemptLoop(ctx, reviewerWT, rnd.prNumber, rnd.currentRound)
	if failEvent != "" {
		return failEvent
	}

	if err := r.checkFreshnessAndSubmit(ctx, finalState, rnd); err != nil {
		return loop.EventReviewFailed
	}

	reviewerWorktreePath := filepath.Join(ctx.GolemicDir, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))
	isDirty, err := worktree.IsDirty(reviewerWorktreePath, r.executor)
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: failed to check dirty status: %v\n", err) //nolint:errcheck
		return loop.EventReviewFailed
	}
	if isDirty {
		fmt.Fprintf(r.stderr, "review_failed: reviewer worktree has uncommitted changes\n") //nolint:errcheck
		return loop.EventReviewFailed
	}

	ctx.Round = r.countReviewSubmittedEvents(ctx.EventLogPath)
	if ctx.InMergeReReview {
		ctx.MergeReReviewRound++
	}
	return r.reviewerVerdictEvent(ctx, rnd.roundHeadSHA)
}

// checkFreshnessAndSubmit enforces the freshness gate and submits the review.
func (r *Runner) checkFreshnessAndSubmit(ctx *RunContext, finalState *reviewerInvocationState, rnd reviewerRound) error {
	hasBrokerSubmit := finalState != nil && finalState.reviewSubmitParams != nil
	countAfter := r.countReviewSubmittedEvents(ctx.EventLogPath)
	if !hasBrokerSubmit && countAfter <= rnd.countBefore {
		stateErr := &loop.StateError{
			Step:  loop.StepRunReviewer,
			Event: loop.EventReviewFailed,
			Msg:   "predicate \"gm_review_submit\" unmet: no fresh gm_review_submit recorded in this round",
		}
		fmt.Fprintf(r.stderr, "review_failed: %v\n", stateErr) //nolint:errcheck
		return stateErr
	}
	if err := r.submitReviewAndWriteEvent(finalState, ctx.EventLogPath, rnd.currentRound, rnd.roundHeadSHA); err != nil {
		fmt.Fprintf(r.stderr, "review_failed: %v\n", err) //nolint:errcheck
		return err
	}
	return nil
}

// runReviewerAttemptLoop runs the bounded reviewer-attempt loop (max 3 per round).
// Returns (state, "") on success or (nil, eventKey) on any failure.
func (r *Runner) runReviewerAttemptLoop(ctx *RunContext, reviewerWT string, prNumber, currentRound int) (*reviewerInvocationState, loop.EventKey) {
	const maxReviewerAttempts = 3
	prevGateRejected := false
	var prevGateMsg string
	var finalState *reviewerInvocationState

	for attempt := 0; attempt < maxReviewerAttempts; attempt++ {
		if !prevGateRejected {
			if err := r.sweepPendingReviews(prNumber); err != nil {
				fmt.Fprintf(r.stderr, "%v\n", err) //nolint:errcheck
				return nil, loop.EventReviewFailed
			}
		}

		precheckBlock, precheckResult, precheckErr := r.runReviewerPrecheck(reviewerWT, ctx.EventLogPath, ctx.ParentSpanID)
		if precheckErr != nil {
			fmt.Fprintf(r.stderr, "review_failed: %v\n", precheckErr) //nolint:errcheck
			return nil, loop.EventReviewFailed
		}

		if precheckNotOK(precheckResult) {
			return nil, r.reviewerPrecheckEvent(ctx, currentRound, precheckResult)
		}

		var gateRetryReason string
		if prevGateRejected {
			gateRetryReason = prevGateMsg
		}

		outcome, state := r.runReviewerAgent(ctx.GolemicDir, ctx.EventLogPath, ctx.Timeout, ctx.ParentSpanID, currentRound, attempt, precheckBlock, precheckResult, gateRetryReason)
		finalState = state

		if reviewGateRejected(state) {
			prevGateRejected = true
			prevGateMsg = state.reviewSubmitGateMsg
			if attempt == maxReviewerAttempts-1 {
				fmt.Fprintf(r.stderr, "review_failed: reviewer gate rejected all %d attempts for this round\n", maxReviewerAttempts) //nolint:errcheck
				return nil, loop.EventReviewFailed
			}
			continue
		}

		if outcome != outcomeSuccess {
			return nil, reviewerAgentOutcomeToEvent(outcome)
		}
		break
	}
	return finalState, ""
}

// reviewerPrecheckEvent handles a !ok precheck: writes a synthetic review_submitted event,
// updates ctx.Round (and MergeReReviewRound when in merge re-review), then escalates
// or configures findings for a dev-retry.
func (r *Runner) reviewerPrecheckEvent(ctx *RunContext, currentRound int, res *reviewerPrecheckResult) loop.EventKey {
	if err := r.writePrecheckReviewSubmittedEvent(ctx.EventLogPath, currentRound); err != nil {
		fmt.Fprintf(r.stderr, "review_failed: write precheck review_submitted: %v\n", err) //nolint:errcheck
		return loop.EventReviewFailed
	}
	ctx.Round = r.countReviewSubmittedEvents(ctx.EventLogPath)
	var escalate bool
	if ctx.InMergeReReview {
		ctx.MergeReReviewRound++
		escalate = ctx.MergeReReviewRound >= ctx.MaxMergeReReviewRounds
	} else {
		escalate = ctx.Round >= ctx.MaxRounds
	}
	if escalate {
		r.postEscalationCommentWithSpan(ctx.EventLogPath, ctx.ParentSpanID, ctx.Round)
		return loop.EventPrecheckFailed
	}
	ctx.Findings = buildPrecheckFindings(res)
	ctx.FindingsJSON = ""
	ctx.DevMode = DevModeRetryWithFindings
	ctx.DevAttempt = 0
	return loop.EventPrecheckFailed
}

// reviewerVerdictEvent classifies the latest review verdict into a loop event.
// For changes_requested it loads findings into ctx and configures a dev-retry.
func (r *Runner) reviewerVerdictEvent(ctx *RunContext, roundHeadSHA string) loop.EventKey {
	verdict, err := r.latestReviewVerdict(ctx.EventLogPath, ctx.Round, roundHeadSHA)
	if err != nil {
		fmt.Fprintf(r.stderr, "review_failed: review_submitted event missing or invalid\n") //nolint:errcheck
		return loop.EventReviewFailed
	}
	switch verdict {
	case "approved":
		return loop.EventReviewApproved
	case "changes_requested":
		var escalate bool
		if ctx.InMergeReReview {
			escalate = ctx.MergeReReviewRound >= ctx.MaxMergeReReviewRounds
		} else {
			escalate = ctx.Round >= ctx.MaxRounds
		}
		if escalate {
			r.postEscalationCommentWithSpan(ctx.EventLogPath, ctx.ParentSpanID, ctx.Round)
			return loop.EventChangesRequested
		}
		findings, bodyErr := r.latestReviewBody(ctx.EventLogPath)
		if bodyErr != nil || findings == "" {
			fmt.Fprintf(r.stderr, "review_failed: EMPTY_FINDINGS: changes_requested review has an empty body\n") //nolint:errcheck
			return loop.EventReviewFailed
		}
		findingsJSON, findingsErr := r.buildFindingsJSON(ctx.EventLogPath)
		if findingsErr != nil {
			fmt.Fprintf(r.stderr, "review_failed: %v\n", findingsErr) //nolint:errcheck
			return loop.EventReviewFailed
		}
		ctx.Findings = findings
		ctx.FindingsJSON = findingsJSON
		ctx.DevMode = DevModeRetryWithFindings
		ctx.DevAttempt = 0
		return loop.EventChangesRequested
	default:
		fmt.Fprintf(r.stderr, "review_failed: unknown verdict %q\n", verdict) //nolint:errcheck
		return loop.EventReviewFailed
	}
}

func reviewerAgentOutcomeToEvent(outcome string) loop.EventKey {
	if ev, ok := agentFailureEvent(outcome); ok {
		return ev
	}
	return loop.EventReviewFailed
}
