package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/worktree"
)

// prInfo holds PR data retrieved from GitHub for the resume flow.
type prInfo struct {
	Number int
	URL    string
	State  string   // "OPEN", "CLOSED", or "MERGED"
	Labels []string // label names
}

// githubReview holds one submitted review from the GitHub REST API.
type githubReview struct {
	DatabaseID  int    // integer database ID (used in REST paths)
	State       string // "APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED"
	Body        string
	AuthorLogin string
}

func (g *githubReview) databaseIDStr() string { return strconv.Itoa(g.DatabaseID) }

// fetchOpenPRForResume queries GitHub for all PRs (any state) on the issue branch.
// Errors if 0 or >1 PRs are found.
func (r *Runner) fetchOpenPRForResume() (*prInfo, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.DevToken()},
		r.repoRoot,
		"gh", "pr", "list",
		"--head", r.branchName,
		"--json", "number,url,state,labels",
		"--state", "all",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list PRs for resume: %w", err)
	}

	var prs []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("failed to parse PR list for resume: %w", err)
	}

	if len(prs) == 0 {
		return nil, fmt.Errorf("resume requires an existing PR for branch %s", r.branchName)
	}
	if len(prs) > 1 {
		nums := make([]string, len(prs))
		for i, p := range prs {
			nums[i] = fmt.Sprintf("#%d", p.Number)
		}
		return nil, fmt.Errorf("resume: multiple PRs found for branch %s (%s); close or merge extras first",
			r.branchName, strings.Join(nums, ", "))
	}

	pr := prs[0]
	labels := make([]string, len(pr.Labels))
	for i, l := range pr.Labels {
		labels[i] = l.Name
	}
	return &prInfo{Number: pr.Number, URL: pr.URL, State: pr.State, Labels: labels}, nil
}

// fetchBotLogin returns the GitHub login for the reviewer bot's token.
func (r *Runner) fetchBotLogin() (string, error) {
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", "user",
	)
	if err != nil {
		return "", fmt.Errorf("failed to fetch bot login: %w", err)
	}
	var v struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", fmt.Errorf("failed to parse bot login: %w", err)
	}
	if v.Login == "" {
		return "", fmt.Errorf("bot login is empty")
	}
	return v.Login, nil
}

// fetchSubmittedGitHubReviews fetches GitHub's review list for the PR via the REST API.
func (r *Runner) fetchSubmittedGitHubReviews(prNumber int) ([]githubReview, error) {
	nwo, err := r.repoNWO()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repo: %w", err)
	}

	path := fmt.Sprintf("repos/%s/pulls/%d/reviews", nwo, prNumber)
	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", path,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch reviews for PR #%d: %w", prNumber, err)
	}

	var raw []struct {
		ID    int    `json:"id"`
		State string `json:"state"`
		Body  string `json:"body"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse reviews for PR #%d: %w", prNumber, err)
	}

	reviews := make([]githubReview, 0, len(raw))
	for _, rv := range raw {
		reviews = append(reviews, githubReview{
			DatabaseID:  rv.ID,
			State:       rv.State,
			Body:        rv.Body,
			AuthorLogin: rv.User.Login,
		})
	}
	return reviews, nil
}

// countBotChangesRequestedReviews counts CHANGES_REQUESTED reviews authored by botLogin (BR-R5).
func countBotChangesRequestedReviews(reviews []githubReview, botLogin string) int {
	n := 0
	for _, rv := range reviews {
		if rv.State == "CHANGES_REQUESTED" && rv.AuthorLogin == botLogin {
			n++
		}
	}
	return n
}

// pendingReviewProof is one pending review returned by the dedicated resume proof query.
type pendingReviewProof struct {
	ID          string
	AuthorLogin string
}

// graphqlResumeDiscoverPending explicitly requests pending reviews on the PR for resume.
const graphqlResumeDiscoverPending = `query($owner:String!,$name:String!,$prNumber:Int!){repository(owner:$owner,name:$name){pullRequest(number:$prNumber){reviews(first:20,states:[PENDING]){nodes{id author{login}}}}}}`

// resumePendingReviewProofResponse is the GraphQL response shape for resume pending-review proof.
type resumePendingReviewProofResponse struct {
	Data *struct {
		Repository *struct {
			PullRequest *struct {
				Reviews *struct {
					Nodes *[]struct {
						ID     string `json:"id"`
						Author *struct {
							Login string `json:"login"`
						} `json:"author"`
					} `json:"nodes"`
				} `json:"reviews"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// fetchResumePendingReviews returns an authoritative list of PENDING reviews for resume.
func (r *Runner) fetchResumePendingReviews(prNumber int) ([]pendingReviewProof, error) {
	nwo, err := r.repoNWO()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve repo for pending review proof: %w", err)
	}
	parts := strings.SplitN(nwo, "/", 2)
	owner, repoName := parts[0], parts[1]

	out, err := r.executor.RunWithEnvInDir(
		map[string]string{"GH_TOKEN": r.creds.ReviewerToken()},
		r.repoRoot,
		"gh", "api", "graphql",
		"-f", "query="+graphqlResumeDiscoverPending,
		"-f", "owner="+owner,
		"-f", "name="+repoName,
		"-F", fmt.Sprintf("prNumber=%d", prNumber),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to prove pending reviews for PR #%d: %w", prNumber, err)
	}

	var resp resumePendingReviewProofResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse pending review proof for PR #%d: %w", prNumber, err)
	}
	return resumePendingReviewsFromProofResponse(&resp, prNumber)
}

// resumePendingReviewsFromProofResponse validates and converts the pending-review proof response.
func resumePendingReviewsFromProofResponse(resp *resumePendingReviewProofResponse, prNumber int) ([]pendingReviewProof, error) {
	if resp.Data == nil || resp.Data.Repository == nil || resp.Data.Repository.PullRequest == nil || resp.Data.Repository.PullRequest.Reviews == nil || resp.Data.Repository.PullRequest.Reviews.Nodes == nil {
		return nil, fmt.Errorf("resume: pending review proof unavailable for PR #%d", prNumber)
	}

	nodes := *resp.Data.Repository.PullRequest.Reviews.Nodes
	proofs := make([]pendingReviewProof, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == "" || node.Author == nil || node.Author.Login == "" {
			return nil, fmt.Errorf("resume: pending review proof for PR #%d is ambiguous", prNumber)
		}
		proofs = append(proofs, pendingReviewProof{ID: node.ID, AuthorLogin: node.Author.Login})
	}
	return proofs, nil
}

// resumePrepareReviewerTurn proves pending review state and deletes only bot-owned pending reviews.
func (r *Runner) resumePrepareReviewerTurn(prNumber int, botLogin string) error {
	proofs, err := r.fetchResumePendingReviews(prNumber)
	if err != nil {
		return err
	}

	botPending := 0
	for _, proof := range proofs {
		if proof.AuthorLogin != botLogin {
			return fmt.Errorf("resume: pending review from %q exists; ask them to submit or dismiss it before resuming", proof.AuthorLogin)
		}
		botPending++
	}

	for i := 0; i < botPending; i++ {
		if err := r.sweepPendingReviews(prNumber); err != nil {
			return fmt.Errorf("failed to delete pending review before resume: %w", err)
		}
	}
	return nil
}

// synthesizePROpenedEvent writes a synthetic pr_opened event for the existing PR.
func (r *Runner) synthesizePROpenedEvent(writer worktree.EventWriter, prNumber int) error {
	payload, err := json.Marshal(map[string]string{"prNumber": fmt.Sprintf("%d", prNumber)})
	if err != nil {
		return fmt.Errorf("failed to marshal pr_opened payload: %w", err)
	}
	return writer.Write(eventlog.Event{
		Type:    eventlog.EventPROpened,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  r.turnCounter,
		Payload: payload,
	})
}

// synthesizeReviewSubmittedEvent writes a synthetic review_submitted event.
// Used to pre-populate the bot-round count and to carry merge confidence for the APPROVED case.
func (r *Runner) synthesizeReviewSubmittedEvent(writer worktree.EventWriter, prNumber int, verdict, confidence, reviewID string) error {
	inlineCount := 0
	payload, err := json.Marshal(map[string]interface{}{
		"verdict":            verdict,
		"body":               "",
		"prNumber":           prNumber,
		"confidence":         confidence,
		"reviewId":           reviewID,
		"inlineCommentCount": &inlineCount,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal review_submitted payload: %w", err)
	}
	return writer.Write(eventlog.Event{
		Type:    eventlog.EventReviewSubmitted,
		Ts:      time.Now().Format(time.RFC3339),
		RunID:   r.runID,
		TurnID:  r.turnCounter,
		Payload: payload,
	})
}

// buildFindingsJSONForReview loads inline comments for a specific GitHub review and serializes them.
// reviewDatabaseID must be the integer database ID as returned by the REST API.
func (r *Runner) buildFindingsJSONForReview(prNumber int, reviewDatabaseID string) (string, error) {
	entries, err := r.loadInlineComments(prNumber, reviewDatabaseID)
	if err != nil {
		return "", fmt.Errorf("failed to load inline comments for review %s: %w", reviewDatabaseID, err)
	}
	if len(entries) == 0 {
		return "", nil
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("failed to marshal inline comments: %w", err)
	}
	return string(b), nil
}

// confidenceFromLabels infers confidence from PR labels (BR-R8).
func confidenceFromLabels(labels []string) string {
	for _, l := range labels {
		switch l {
		case "confidence:low":
			return "low"
		case "confidence:medium":
			return "medium"
		case "confidence:high":
			return "high"
		}
	}
	return "low"
}

// applyRunMode copies runner mode into the shared context.
func (r *Runner) applyRunMode(ctx *RunContext) { ctx.Resume = r.resume }

// hydrateResume reconstructs RunContext and synthesized events from GitHub state.
func (r *Runner) hydrateResume(ctx *RunContext) error {
	ctx.PrepareFailKind = ""

	pr, botLogin, merged, validateErr := r.resumeValidate(ctx.Writer)
	if validateErr != "" {
		return fmt.Errorf("%s", validateErr)
	}
	if merged {
		return r.hydrateMergedResume(ctx)
	}
	return r.hydrateOpenResume(ctx, pr, botLogin)
}

func (r *Runner) hydrateMergedResume(ctx *RunContext) error {
	ctx.Round = 0
	ctx.ResumeVerdict = "approved"
	ctx.PRState = "MERGED"
	return nil
}

func (r *Runner) hydrateOpenResume(ctx *RunContext, pr *prInfo, botLogin string) error {
	if o := r.ensureDevWorktreeForResume(ctx.Writer); o != "" {
		return fmt.Errorf("resume: failed to ensure dev worktree")
	}

	if synthErr := r.synthesizePROpenedEvent(ctx.Writer, pr.Number); synthErr != nil {
		fmt.Fprintf(r.stderr, "resume: failed to write pr_opened event: %v\n", synthErr)
		return synthErr
	}

	allReviews, err := r.fetchSubmittedGitHubReviews(pr.Number)
	if err != nil {
		fmt.Fprintf(r.stderr, "resume: failed to fetch reviews for PR #%d: %v\n", pr.Number, err)
		return err
	}

	return r.hydrateResumeReviews(ctx, pr, botLogin, allReviews)
}

func (r *Runner) hydrateResumeReviews(ctx *RunContext, pr *prInfo, botLogin string, allReviews []githubReview) error {
	submittedReviews := filterDecisionReviews(allReviews)
	ctx.Round = countBotChangesRequestedReviews(submittedReviews, botLogin)
	ctx.PRState = ""

	if len(submittedReviews) == 0 {
		return r.hydrateResumeNoDecision(ctx, pr, botLogin)
	}
	return r.hydrateResumeDecision(ctx, pr, botLogin, submittedReviews)
}

func (r *Runner) hydrateResumeNoDecision(ctx *RunContext, pr *prInfo, botLogin string) error {
	if o := r.runCIGate(pr.Number, ctx.EventLogPath, ctx.Timeout); o != outcomeSuccess {
		ctx.PrepareFailKind = "dev_failed"
		return fmt.Errorf("resume: CI gate failed")
	}
	if err := r.resumePrepareReviewerTurn(pr.Number, botLogin); err != nil {
		fmt.Fprintf(r.stderr, "%v\n", err)
		return err
	}
	ctx.ReviewerWorktreeExists = true
	ctx.ResumeVerdict = ""
	return nil
}

func (r *Runner) hydrateResumeDecision(ctx *RunContext, pr *prInfo, botLogin string, submittedReviews []githubReview) error {
	lastReview := submittedReviews[len(submittedReviews)-1]
	switch lastReview.State {
	case "CHANGES_REQUESTED":
		return r.hydrateResumeChangesRequested(ctx, pr, botLogin, submittedReviews, lastReview)
	case "APPROVED":
		return r.hydrateResumeApproved(ctx, pr, lastReview)
	default:
		fmt.Fprintf(r.stderr, "resume: last submitted review has unhandled state %q\n", lastReview.State)
		return fmt.Errorf("resume: unhandled review state %q", lastReview.State)
	}
}

func (r *Runner) hydrateResumeChangesRequested(ctx *RunContext, pr *prInfo, botLogin string, submittedReviews []githubReview, lastReview githubReview) error {
	ctx.ResumeVerdict = "changes_requested"
	if err := r.resumeSynthesizeBotCREvents(ctx.Writer, pr.Number, submittedReviews, botLogin); err != nil {
		fmt.Fprintf(r.stderr, "resume: failed to synthesize review event: %v\n", err)
		return err
	}
	if ctx.Round >= r.cfg.MaxReviewRounds {
		r.postEscalationCommentWithSpan(ctx.EventLogPath, ctx.ParentSpanID, ctx.Round)
		ctx.PrepareFailKind = "escalated"
		return fmt.Errorf("resume: review rounds exhausted")
	}

	findings, findingsJSON, err := r.hydrateResumeFindings(pr, lastReview)
	if err != nil {
		return err
	}
	if findings == "" {
		findings = "See inline review comments"
	}

	ctx.Round = ctx.Round + 1
	ctx.Findings = findings
	ctx.FindingsJSON = findingsJSON
	ctx.DevMode = DevModeRetryWithFindings
	ctx.DevAttempt = 0
	ctx.ReviewerWorktreeExists = true
	return nil
}

func (r *Runner) hydrateResumeFindings(pr *prInfo, lastReview githubReview) (string, string, error) {
	findings := lastReview.Body
	findingsJSON, err := r.buildFindingsJSONForReview(pr.Number, lastReview.databaseIDStr())
	if err != nil {
		fmt.Fprintf(r.stderr, "resume: failed to load inline comments for review #%d: %v\n", lastReview.DatabaseID, err)
		return "", "", err
	}
	if findings == "" && findingsJSON == "" {
		fmt.Fprintf(r.stderr,
			"resume: CHANGES_REQUESTED review #%d has no findings (empty body and no inline comments)\n",
			lastReview.DatabaseID)
		return "", "", fmt.Errorf("resume: empty findings")
	}
	return findings, findingsJSON, nil
}

func (r *Runner) hydrateResumeApproved(ctx *RunContext, pr *prInfo, lastReview githubReview) error {
	ctx.ResumeVerdict = "approved"
	confidence := confidenceFromLabels(pr.Labels)
	if synthErr := r.synthesizeReviewSubmittedEvent(ctx.Writer, pr.Number, "approved", confidence, lastReview.databaseIDStr()); synthErr != nil {
		fmt.Fprintf(r.stderr, "resume: failed to write review_submitted event: %v\n", synthErr)
		return synthErr
	}
	return nil
}

// resumeValidate fetches and validates the PR for resume, checking state, remote branch, bot login,
// and human pending reviews. Returns the PR info, bot login, a merged flag, and a non-empty outcome on failure.
func (r *Runner) resumeValidate(writer worktree.EventWriter) (*prInfo, string, bool, string) {
	pr, err := r.fetchOpenPRForResume()
	if err != nil {
		fmt.Fprintf(r.stderr, "resume: %v\n", err)
		return nil, "", false, outcomeAborted
	}

	switch pr.State {
	case "MERGED":
		if synthErr := r.synthesizePROpenedEvent(writer, pr.Number); synthErr != nil {
			fmt.Fprintf(r.stderr, "resume: failed to write pr_opened event: %v\n", synthErr)
			return nil, "", false, outcomeAborted
		}
		fmt.Fprintf(r.stderr, "resume: PR #%d is already merged; nothing to do\n", pr.Number)
		return pr, "", true, ""
	case "CLOSED":
		fmt.Fprintf(r.stderr, "resume: PR #%d is closed but not merged; manual intervention required\n", pr.Number)
		return nil, "", false, outcomeAborted
	case "OPEN":
	default:
		fmt.Fprintf(r.stderr, "resume: PR #%d has unexpected state %q\n", pr.Number, pr.State)
		return nil, "", false, outcomeAborted
	}

	remoteOut, gitErr := r.executor.RunInDir(r.repoRoot, "git", "ls-remote", "--heads", "origin", r.branchName)
	if gitErr != nil {
		fmt.Fprintf(r.stderr, "resume: failed to check remote branch: %v\n", gitErr)
		return nil, "", false, outcomeAborted
	}
	if strings.TrimSpace(remoteOut) == "" {
		fmt.Fprintf(r.stderr, "resume: remote branch %s not found on origin; cannot resume\n", r.branchName)
		return nil, "", false, outcomeAborted
	}

	botLogin, err := r.fetchBotLogin()
	if err != nil {
		fmt.Fprintf(r.stderr, "resume: %v\n", err)
		return nil, "", false, outcomeAborted
	}

	return pr, botLogin, false, ""
}

// ensureDevWorktreeForResume guarantees a dev worktree on the PR branch exists
// before the resume flow delegates to functions that operate on devWorktreePath().
// All git plumbing lives in the worktree package (worktree.EnsureForResume);
// this only maps failures to the resume outcome. Returns "" on success or
// outcomeAborted on failure.
func (r *Runner) ensureDevWorktreeForResume(writer worktree.EventWriter) string {
	golemicDir := filepath.Join(r.homeDir, ".golemic", r.project)
	if err := worktree.EnsureForResume(r.repoRoot, golemicDir, r.runID, r.issueNum, "golemic-dev", r.executor, writer, r.turnCounter); err != nil {
		fmt.Fprintf(r.stderr, "resume: %v\n", err)
		return outcomeAborted
	}
	return ""
}

// filterDecisionReviews returns only APPROVED and CHANGES_REQUESTED reviews.
func filterDecisionReviews(reviews []githubReview) []githubReview {
	out := make([]githubReview, 0, len(reviews))
	for _, rv := range reviews {
		if rv.State == "APPROVED" || rv.State == "CHANGES_REQUESTED" {
			out = append(out, rv)
		}
	}
	return out
}

// resumeSynthesizeBotCREvents writes a review_submitted event for each bot CHANGES_REQUESTED review.
func (r *Runner) resumeSynthesizeBotCREvents(writer worktree.EventWriter, prNumber int, submittedReviews []githubReview, botLogin string) error {
	for _, rv := range submittedReviews {
		if rv.State == "CHANGES_REQUESTED" && rv.AuthorLogin == botLogin {
			if err := r.synthesizeReviewSubmittedEvent(writer, prNumber, "changes_requested", "high", rv.databaseIDStr()); err != nil {
				return err
			}
		}
	}
	return nil
}
