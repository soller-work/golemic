package selector

import (
	"fmt"
	"strings"
	"testing"

	"golemic/internal/preflight"
)

// fakeFetch is a helper that builds a candidate from simple fields.
func mkCandidate(number int, labels []string, blockedBy, closingPRs int) candidate {
	inProgress := false
	for _, l := range labels {
		if l == "in-progress" {
			inProgress = true
		}
	}
	return candidate{
		Number:         number,
		Title:          fmt.Sprintf("Issue #%d", number),
		URL:            fmt.Sprintf("https://github.com/owner/repo/issues/%d", number),
		Labels:         labels,
		InProgress:     inProgress,
		BlockedByCount: blockedBy,
		ClosingPRCount: closingPRs,
	}
}

func TestFilter_InProgressExcluded(t *testing.T) {
	candidates := []candidate{
		mkCandidate(7, []string{"ready-for-agent", "in-progress"}, 0, 0),
		mkCandidate(9, []string{"ready-for-agent"}, 0, 0),
	}
	got := filter(candidates)
	if len(got) != 1 || got[0].Number != 9 {
		t.Errorf("expected only #9, got %v", got)
	}
}

func TestFilter_BlockedByExcluded(t *testing.T) {
	candidates := []candidate{
		mkCandidate(12, []string{"ready-for-agent"}, 1, 0),
		mkCandidate(20, []string{"ready-for-agent"}, 0, 0),
	}
	got := filter(candidates)
	if len(got) != 1 || got[0].Number != 20 {
		t.Errorf("expected only #20, got %v", got)
	}
}

func TestFilter_ClosingPRExcluded(t *testing.T) {
	candidates := []candidate{
		mkCandidate(15, []string{"ready-for-agent"}, 0, 1),
		mkCandidate(22, []string{"ready-for-agent"}, 0, 0),
	}
	got := filter(candidates)
	if len(got) != 1 || got[0].Number != 22 {
		t.Errorf("expected only #22, got %v", got)
	}
}

func TestFilter_AllExcluded(t *testing.T) {
	candidates := []candidate{
		mkCandidate(1, []string{"ready-for-agent", "in-progress"}, 0, 0),
		mkCandidate(2, []string{"ready-for-agent"}, 2, 0),
	}
	got := filter(candidates)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestSelectTop_BugWinsOverLowerNumber(t *testing.T) {
	candidates := []candidate{
		mkCandidate(10, []string{"ready-for-agent"}, 0, 0),
		mkCandidate(50, []string{"ready-for-agent", "bug"}, 0, 0),
	}
	got := selectTop(candidates)
	if got == nil || got.Number != 50 {
		t.Errorf("expected #50 (bug), got %v", got)
	}
}

func TestSelectTop_TiebreakerLowerNumberWins(t *testing.T) {
	candidates := []candidate{
		mkCandidate(23, []string{"ready-for-agent", "bug"}, 0, 0),
		mkCandidate(17, []string{"ready-for-agent", "bug"}, 0, 0),
	}
	got := selectTop(candidates)
	if got == nil || got.Number != 17 {
		t.Errorf("expected #17, got %v", got)
	}
}

func TestSelectTop_NoBug_LowerNumberWins(t *testing.T) {
	candidates := []candidate{
		mkCandidate(42, []string{"ready-for-agent"}, 0, 0),
		mkCandidate(10, []string{"ready-for-agent"}, 0, 0),
	}
	got := selectTop(candidates)
	if got == nil || got.Number != 10 {
		t.Errorf("expected #10, got %v", got)
	}
}

func TestSelectTop_Empty(t *testing.T) {
	got := selectTop(nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestSelectTop_AllFiltered(t *testing.T) {
	candidates := []candidate{
		mkCandidate(5, []string{"ready-for-agent", "in-progress"}, 0, 0),
	}
	got := selectTop(candidates)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// fakeExec is a minimal preflight.Executor for Fetch tests.
type fakeExec struct {
	runWithEnvFunc func(env map[string]string, name string, args ...string) (string, error)
}

func (f fakeExec) Run(name string, args ...string) (string, error) {
	return "", fmt.Errorf("not mocked")
}

func (f fakeExec) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	if f.runWithEnvFunc != nil {
		return f.runWithEnvFunc(env, name, args...)
	}
	return "", fmt.Errorf("not mocked")
}

func (f fakeExec) RunInDir(_ string, name string, args ...string) (string, error) {
	return f.Run(name, args...)
}

func (f fakeExec) RunWithEnvInDir(env map[string]string, _ string, name string, args ...string) (string, error) {
	return f.RunWithEnv(env, name, args...)
}

const fixtureGraphQLResponse = `{
  "data": {
    "repository": {
      "issues": {
        "nodes": [
          {
            "number": 42,
            "title": "Fix the bug",
            "url": "https://github.com/owner/repo/issues/42",
            "labels": {"nodes": [{"name": "ready-for-agent"}]},
            "trackedIssues": {"totalCount": 0},
            "closedByPullRequestsReferences": {"nodes": []}
          }
        ]
      }
    }
  }
}`

func TestFetch_ParsesCanonicalResponse(t *testing.T) {
	exec := fakeExec{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return fixtureGraphQLResponse, nil
		},
	}

	candidates, err := Fetch(exec, "owner/repo", "ghp_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	c := candidates[0]
	if c.Number != 42 {
		t.Errorf("number: got %d, want 42", c.Number)
	}
	if c.Title != "Fix the bug" {
		t.Errorf("title: got %q", c.Title)
	}
	if c.BlockedByCount != 0 {
		t.Errorf("blockedBy: got %d", c.BlockedByCount)
	}
	if c.ClosingPRCount != 0 {
		t.Errorf("closingPR: got %d", c.ClosingPRCount)
	}
	if len(c.Labels) != 1 || c.Labels[0] != "ready-for-agent" {
		t.Errorf("labels: got %v", c.Labels)
	}
}

func TestFetch_TokenInjectedViaGHToken(t *testing.T) {
	var capturedToken string
	exec := fakeExec{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			capturedToken = env["GH_TOKEN"]
			return fixtureGraphQLResponse, nil
		},
	}

	_, err := Fetch(exec, "owner/repo", "ghp_dev_xxx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedToken != "ghp_dev_xxx" {
		t.Errorf("GH_TOKEN: got %q, want %q", capturedToken, "ghp_dev_xxx")
	}
}

func TestFetch_First50InQuery(t *testing.T) {
	var capturedQuery string
	exec := fakeExec{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			for _, arg := range args {
				if strings.HasPrefix(arg, "query=") {
					capturedQuery = strings.TrimPrefix(arg, "query=")
				}
			}
			return fixtureGraphQLResponse, nil
		},
	}

	_, err := Fetch(exec, "owner/repo", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedQuery, "first: 50") {
		t.Errorf("GraphQL query does not contain 'first: 50'; got: %s", capturedQuery)
	}
	if strings.Contains(capturedQuery, "cursor") || strings.Contains(capturedQuery, "after:") {
		t.Errorf("GraphQL query must not contain pagination cursor; got: %s", capturedQuery)
	}
}

func TestFetch_ExecutorErrorSurfaces(t *testing.T) {
	exec := fakeExec{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "", &preflight.ErrExit{ExitCode: 1, Stderr: "HTTP 500"}
		},
	}

	_, err := Fetch(exec, "owner/repo", "tok")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "gh api graphql failed") {
		t.Errorf("error should start with 'gh api graphql failed'; got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should contain 'HTTP 500'; got: %v", err)
	}
}

func TestFetch_MalformedJSONSurfaces(t *testing.T) {
	exec := fakeExec{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			return "not json {{", nil
		},
	}

	_, err := Fetch(exec, "owner/repo", "tok")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "gh api graphql failed") {
		t.Errorf("error should start with 'gh api graphql failed'; got: %v", err)
	}
}

func TestFetch_InvalidRepoSlug(t *testing.T) {
	exec := fakeExec{}
	_, err := Fetch(exec, "noslash", "tok")
	if err == nil {
		t.Fatal("expected error for invalid slug")
	}
}
