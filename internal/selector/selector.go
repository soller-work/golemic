// Package selector implements the next-takeable-issue query for the golemic runner.
package selector

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"golemic/internal/preflight"
)

// Issue is the output model returned by Select and serialised to stdout.
type Issue struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	URL    string   `json:"url"`
	Labels []string `json:"labels"`
}

// candidate is the internal representation parsed from the GraphQL response.
type candidate struct {
	Number         int
	Title          string
	URL            string
	Labels         []string
	InProgress     bool
	BlockedByCount int
	ClosingPRCount int
}

// graphqlResponse is the top-level structure of the gh api graphql output.
type graphqlResponse struct {
	Data struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					Number int    `json:"number"`
					Title  string `json:"title"`
					URL    string `json:"url"`
					Labels struct {
						Nodes []struct {
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"labels"`
					TrackedIssues struct {
						TotalCount int `json:"totalCount"`
					} `json:"trackedIssues"`
					ClosingIssuesReferences struct {
						TotalCount int `json:"totalCount"`
					} `json:"closingIssuesReferences"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	} `json:"data"`
}

const graphqlQuery = `query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    issues(first: 50, states: OPEN, labels: ["ready-for-agent"], orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes {
        number
        title
        url
        labels(first: 20) {
          nodes { name }
        }
        trackedIssues { totalCount }
        closingIssuesReferences(first: 1, states: OPEN) { totalCount }
      }
    }
  }
}`

// Fetch runs the GraphQL query against GitHub using the given executor and token,
// resolving owner and repo from repoSlug ("owner/repo").
// Returns a slice of candidates on success; returns an error on gh failure or JSON parse failure.
func Fetch(executor preflight.Executor, repoSlug, token string) ([]candidate, error) {
	parts := strings.SplitN(repoSlug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid repo slug %q: expected owner/repo", repoSlug)
	}
	owner, repoName := parts[0], parts[1]

	out, err := executor.RunWithEnv(
		map[string]string{"GH_TOKEN": token},
		"gh", "api", "graphql",
		"-f", "query="+graphqlQuery,
		"-f", "owner="+owner,
		"-f", "repo="+repoName,
	)
	if err != nil {
		var ee *preflight.ErrExit
		if isErrExit(err, &ee) {
			return nil, fmt.Errorf("gh api graphql failed: %s", strings.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("gh api graphql failed: %w", err)
	}

	var resp graphqlResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, fmt.Errorf("gh api graphql failed: parse error: %w", err)
	}

	nodes := resp.Data.Repository.Issues.Nodes
	candidates := make([]candidate, 0, len(nodes))
	for _, n := range nodes {
		labels := make([]string, 0, len(n.Labels.Nodes))
		inProgress := false
		for _, l := range n.Labels.Nodes {
			labels = append(labels, l.Name)
			if l.Name == "in-progress" {
				inProgress = true
			}
		}
		candidates = append(candidates, candidate{
			Number:         n.Number,
			Title:          n.Title,
			URL:            n.URL,
			Labels:         labels,
			InProgress:     inProgress,
			BlockedByCount: n.TrackedIssues.TotalCount,
			ClosingPRCount: n.ClosingIssuesReferences.TotalCount,
		})
	}
	return candidates, nil
}

// isErrExit checks whether err is a *preflight.ErrExit and fills ee.
func isErrExit(err error, ee **preflight.ErrExit) bool {
	if err == nil {
		return false
	}
	// type assertion — preflight.ErrExit is not in errors chain, just direct
	if e, ok := err.(*preflight.ErrExit); ok { //nolint:errorlint
		*ee = e
		return true
	}
	return false
}

// filter returns only takeable candidates per BR-001.
func filter(candidates []candidate) []candidate {
	out := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.InProgress {
			continue
		}
		if c.BlockedByCount > 0 {
			continue
		}
		if c.ClosingPRCount > 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

// selectTop filters and sorts candidates, returning the single best Issue per BR-001/BR-002.
// Returns nil when no takeable candidate exists.
func selectTop(candidates []candidate) *Issue {
	takeable := filter(candidates)
	if len(takeable) == 0 {
		return nil
	}

	sort.SliceStable(takeable, func(i, j int) bool {
		bugI := hasBug(takeable[i].Labels)
		bugJ := hasBug(takeable[j].Labels)
		if bugI != bugJ {
			return bugI // bug-labeled first
		}
		return takeable[i].Number < takeable[j].Number
	})

	top := takeable[0]
	return &Issue{
		Number: top.Number,
		Title:  top.Title,
		URL:    top.URL,
		Labels: top.Labels,
	}
}

func hasBug(labels []string) bool {
	for _, l := range labels {
		if l == "bug" {
			return true
		}
	}
	return false
}

// NextIssue fetches candidates from GitHub and returns the next takeable issue.
// Returns (issue, nil) on hit, (nil, nil) on empty, (nil, err) on failure.
func NextIssue(executor preflight.Executor, repoSlug, token string) (*Issue, error) {
	candidates, err := Fetch(executor, repoSlug, token)
	if err != nil {
		return nil, err
	}
	return selectTop(candidates), nil
}
