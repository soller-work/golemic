package runner

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
)

// TokenUsage holds summed token and turn counts for one agent invocation.
type TokenUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	Turns            int   `json:"turns"`
}

// invocationTokenUsage binds a TokenUsage to a specific role/round/attempt.
type invocationTokenUsage struct {
	Role    string
	Round   int
	Attempt int
	Usage   TokenUsage
}

// piUsage reflects the usage object emitted by pi in message_update events.
type piUsage struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// parseActivityUsage reads the activity.jsonl at path and sums usage from
// all message_update events. A missing file, empty file, or any malformed
// line resolves to zero for the affected fields; the function never errors.
func parseActivityUsage(path string) TokenUsage {
	f, err := os.Open(path)
	if err != nil {
		return TokenUsage{}
	}
	defer f.Close()

	var total TokenUsage
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev struct {
			Type  string          `json:"type"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		if ev.Type != "message_update" || len(ev.Usage) == 0 {
			continue
		}
		var u piUsage
		if json.Unmarshal(ev.Usage, &u) != nil {
			continue
		}
		total.InputTokens += int64(u.Input)
		total.OutputTokens += int64(u.Output)
		total.CacheReadTokens += int64(u.CacheRead)
		total.CacheWriteTokens += int64(u.CacheWrite)
		total.Turns++
	}
	return total
}

// tokenUsageAttrs converts a TokenUsage into span end attributes.
func tokenUsageAttrs(u TokenUsage) map[string]any {
	return map[string]any{
		"input_tokens":       u.InputTokens,
		"output_tokens":      u.OutputTokens,
		"cache_read_tokens":  u.CacheReadTokens,
		"cache_write_tokens": u.CacheWriteTokens,
		"turns":              u.Turns,
	}
}

// wrapEndSpanWithUsage returns a new endSpan that merges token usage attributes
// with any attributes the caller supplies.
func wrapEndSpanWithUsage(endSpan func(string, map[string]any), u TokenUsage) func(string, map[string]any) {
	usageBase := tokenUsageAttrs(u)
	return func(status string, extra map[string]any) {
		merged := make(map[string]any, len(usageBase)+len(extra))
		for k, v := range usageBase {
			merged[k] = v
		}
		for k, v := range extra {
			merged[k] = v
		}
		endSpan(status, merged)
	}
}

// recordTokenUsage appends an invocation's usage to the runner's log.
func (r *Runner) recordTokenUsage(role string, round, attempt int, u TokenUsage) {
	r.tokenUsageLog = append(r.tokenUsageLog, invocationTokenUsage{
		Role:    role,
		Round:   round,
		Attempt: attempt,
		Usage:   u,
	})
}

// buildTokenUsageAggregate groups per-invocation token usage by role and
// round, summing across attempts. The outer key is the role name; the inner
// key is the round number formatted as a decimal string.
func buildTokenUsageAggregate(log []invocationTokenUsage) map[string]map[string]TokenUsage {
	agg := make(map[string]map[string]TokenUsage)
	for _, e := range log {
		if agg[e.Role] == nil {
			agg[e.Role] = make(map[string]TokenUsage)
		}
		roundKey := strconv.Itoa(e.Round)
		cur := agg[e.Role][roundKey]
		cur.InputTokens += e.Usage.InputTokens
		cur.OutputTokens += e.Usage.OutputTokens
		cur.CacheReadTokens += e.Usage.CacheReadTokens
		cur.CacheWriteTokens += e.Usage.CacheWriteTokens
		cur.Turns += e.Usage.Turns
		agg[e.Role][roundKey] = cur
	}
	return agg
}
