package agent

import (
	"fmt"
	"strings"
)

// AttemptSummary records one model chain attempt for diagnostics.
type AttemptSummary struct {
	Model  string
	Reason string // short sanitized category, e.g. "provider limit"
}

// ParseModelChain splits a comma-separated model string into an ordered unique chain.
// Entries are trimmed; empty and duplicate entries are dropped while preserving first-seen order.
// Returns an error if the resulting chain is empty.
func ParseModelChain(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	chain := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		chain = append(chain, p)
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("agent: model chain is empty after parsing %q", raw)
	}
	return chain, nil
}
