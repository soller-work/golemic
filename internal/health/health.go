// Package health classifies run health from telemetry and event data.
package health

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golemic/internal/eventlog"
	"golemic/internal/telemetry"
)

// Status values (DT-001).
const (
	StatusRunning       = "running"
	StatusStalled       = "stalled"
	StatusWedged        = "wedged"
	StatusFinished      = "finished"
	StatusFailed        = "failed"
	StatusIndeterminate = "indeterminate"
)

// Liveness values (SE-001).
const (
	LivenessAlive         = "alive"
	LivenessDead          = "dead"
	LivenessIndeterminate = "indeterminate"
)

// DefaultStalledAfter is used when no per-run timeout or --stalled-after is available.
const DefaultStalledAfter = 30 * time.Minute

// RunHealth is the per-run health verdict (RM-001).
type RunHealth struct {
	RunID         string `json:"run_id"`
	Issue         int    `json:"issue"`
	Status        string `json:"status"`
	CurrentPhase  string `json:"current_phase"`
	AgeOrDuration string `json:"age_or_duration"`
	Outcome       string `json:"outcome"`
	PID           *int   `json:"pid"`
	Liveness      string `json:"liveness"`
}

// LivenessProbe checks whether a process with the given pid is alive.
// Returns LivenessAlive, LivenessDead, or LivenessIndeterminate.
type LivenessProbe func(pid int) string

// OsLivenessProbe is the production liveness probe using signal 0 (BR-008, SE-001).
func OsLivenessProbe(pid int) string {
	p, err := os.FindProcess(pid)
	if err != nil {
		return LivenessDead
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return LivenessAlive
	}
	if errors.Is(err, syscall.ESRCH) {
		return LivenessDead
	}
	if errors.Is(err, syscall.EPERM) {
		return LivenessAlive // process exists, owned by another user
	}
	return LivenessIndeterminate
}

// Classifier reads run directories and classifies run health.
type Classifier struct {
	Probe        LivenessProbe    // nil → OsLivenessProbe
	Now          func() time.Time // nil → time.Now
	StalledAfter time.Duration    // 0 → DefaultStalledAfter
}

func (c *Classifier) probe() LivenessProbe {
	if c.Probe != nil {
		return c.Probe
	}
	return OsLivenessProbe
}

func (c *Classifier) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Classifier) stalledThreshold() time.Duration {
	if c.StalledAfter > 0 {
		return c.StalledAfter
	}
	return DefaultStalledAfter
}

// ClassifyAll scans runsDir and returns health for all runs, sorted newest-first.
// Returns nil slice (not error) when runsDir is empty or missing.
func (c *Classifier) ClassifyAll(runsDir string) ([]RunHealth, error) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("RUNSDIR_READ_FAILED: cannot read runs directory: %w", err)
	}

	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	// Sort by timestamp suffix (newest-first); suffix is lexicographically comparable.
	sort.Slice(dirs, func(i, j int) bool {
		return timestampSuffix(dirs[i]) > timestampSuffix(dirs[j])
	})

	now := c.now()
	result := make([]RunHealth, 0, len(dirs))
	for _, name := range dirs {
		h := c.classifyDir(filepath.Join(runsDir, name), now)
		result = append(result, h)
	}
	return result, nil
}

// ClassifyOne reads a single run directory and returns its health.
// Returns RUN_NOT_FOUND error when the directory does not exist.
func (c *Classifier) ClassifyOne(runDir string) (RunHealth, error) {
	if _, err := os.Stat(runDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RunHealth{}, fmt.Errorf("RUN_NOT_FOUND: %s", filepath.Base(runDir))
		}
		return RunHealth{}, fmt.Errorf("RUNSDIR_READ_FAILED: %w", err)
	}
	return c.classifyDir(runDir, c.now()), nil
}

func (c *Classifier) classifyDir(runDir string, now time.Time) RunHealth {
	runID := filepath.Base(runDir)
	issue := issueFromRunID(runID)

	ev := readEvents(filepath.Join(runDir, "events.jsonl"))
	if ev.Issue != 0 {
		issue = ev.Issue
	}
	tel := readTelemetry(filepath.Join(runDir, "telemetry.jsonl"))
	return c.classify(runID, issue, ev, tel, now)
}

// eventsData holds relevant data from events.jsonl.
type eventsData struct {
	Issue           int
	FinishedOutcome string    // empty if run_finished not seen
	StartTS         time.Time // from run_started ts
}

// telemetryData holds relevant data from telemetry.jsonl.
type telemetryData struct {
	RunSpanPID    *int
	RunStartTS    time.Time
	RunDurationMS *int64
	OpenSpans     []openSpan // in file order (chronological)
}

type openSpan struct {
	Name    string
	StartTS time.Time
}

func (c *Classifier) classify(runID string, issue int, ev eventsData, tel *telemetryData, now time.Time) RunHealth {
	ageStr := computeAge(ev, tel, now)

	pid := (*int)(nil)
	if tel != nil {
		pid = tel.RunSpanPID
	}

	// BR-001 / BR-002: terminal states from events.jsonl
	if ev.FinishedOutcome != "" {
		status := StatusFailed
		if ev.FinishedOutcome == "success" {
			status = StatusFinished
		}
		liveness := LivenessIndeterminate
		if pid != nil {
			liveness = LivenessDead // process has exited (wrote run_finished)
		}
		return RunHealth{
			RunID: runID, Issue: issue,
			Status: status, CurrentPhase: "",
			AgeOrDuration: ageStr, Outcome: ev.FinishedOutcome,
			PID: pid, Liveness: liveness,
		}
	}

	// BR-007: no telemetry or no pid → indeterminate liveness, events-only fallback
	if tel == nil || pid == nil {
		return RunHealth{
			RunID: runID, Issue: issue,
			Status: StatusRunning, CurrentPhase: "-",
			AgeOrDuration: ageStr,
			PID:           nil, Liveness: LivenessIndeterminate,
		}
	}

	// BR-003 / BR-004 / BR-005: classify by liveness and open-span age
	liveness := c.probe()(*pid)
	switch liveness {
	case LivenessDead:
		return RunHealth{
			RunID: runID, Issue: issue,
			Status: StatusWedged, CurrentPhase: "-",
			AgeOrDuration: ageStr,
			PID:           pid, Liveness: LivenessDead,
		}
	case LivenessAlive:
		return c.classifyAlive(runID, issue, pid, ageStr, tel.OpenSpans, now)
	default:
		return RunHealth{
			RunID: runID, Issue: issue,
			Status: StatusIndeterminate, CurrentPhase: "-",
			AgeOrDuration: ageStr,
			PID:           pid, Liveness: LivenessIndeterminate,
		}
	}
}

func (c *Classifier) classifyAlive(runID string, issue int, pid *int, ageStr string, spans []openSpan, now time.Time) RunHealth {
	threshold := c.stalledThreshold()

	currentPhase := "-"
	stalledPhase := ""

	for _, span := range spans {
		currentPhase = span.Name // last open span (most recently started) wins
		if now.Sub(span.StartTS) > threshold && stalledPhase == "" {
			stalledPhase = span.Name
		}
	}

	if stalledPhase != "" {
		return RunHealth{
			RunID: runID, Issue: issue,
			Status: StatusStalled, CurrentPhase: stalledPhase,
			AgeOrDuration: ageStr,
			PID:           pid, Liveness: LivenessAlive,
		}
	}
	return RunHealth{
		RunID: runID, Issue: issue,
		Status: StatusRunning, CurrentPhase: currentPhase,
		AgeOrDuration: ageStr,
		PID:           pid, Liveness: LivenessAlive,
	}
}

// computeAge returns a human-readable age or duration string.
func computeAge(ev eventsData, tel *telemetryData, now time.Time) string {
	if tel != nil && !tel.RunStartTS.IsZero() {
		if ev.FinishedOutcome != "" && tel.RunDurationMS != nil {
			return fmtDuration(time.Duration(*tel.RunDurationMS) * time.Millisecond)
		}
		return fmtDuration(now.Sub(tel.RunStartTS))
	}
	if !ev.StartTS.IsZero() {
		return fmtDuration(now.Sub(ev.StartTS))
	}
	return "-"
}

func readEvents(path string) eventsData {
	events, err := eventlog.Reader{}.Read(path)
	if err != nil {
		return eventsData{}
	}
	var ev eventsData
	for _, e := range events {
		switch e.Type {
		case eventlog.EventRunStarted:
			var p struct {
				Issue int `json:"issue"`
			}
			if json.Unmarshal(e.Payload, &p) == nil {
				ev.Issue = p.Issue
			}
			if ts, tsErr := time.Parse(time.RFC3339, e.Ts); tsErr == nil {
				ev.StartTS = ts
			}
		case eventlog.EventRunFinished:
			var p struct {
				Outcome string `json:"outcome"`
			}
			if json.Unmarshal(e.Payload, &p) == nil && p.Outcome != "" {
				ev.FinishedOutcome = p.Outcome
			}
		}
	}
	return ev
}

func readTelemetry(path string) *telemetryData {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck

	records := parseTelemetryRecords(f)
	if records == nil {
		return nil
	}

	// Index span.end records by span_id for O(1) lookup.
	ends := make(map[string]telemetry.Record, len(records)/2+1)
	for _, r := range records {
		if r.Kind == telemetry.KindSpanEnd {
			ends[r.SpanID] = r
		}
	}

	tel := &telemetryData{}
	for _, rec := range records {
		if rec.Kind != telemetry.KindSpanStart {
			continue
		}
		if rec.Name == telemetry.SpanRun {
			applyRunSpan(rec, ends, tel)
			continue
		}
		if _, hasEnd := ends[rec.SpanID]; !hasEnd {
			if ts, err := time.Parse(time.RFC3339, rec.StartTS); err == nil {
				tel.OpenSpans = append(tel.OpenSpans, openSpan{Name: rec.Name, StartTS: ts})
			}
		}
	}
	return tel
}

// parseTelemetryRecords reads JSONL records from r, tolerating a partial/malformed
// last line (BR-007, mid-write resilience). Returns nil on scanner error.
func parseTelemetryRecords(r io.Reader) []telemetry.Record {
	var records []telemetry.Record
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec telemetry.Record
		if json.Unmarshal([]byte(line), &rec) == nil {
			records = append(records, rec)
		}
	}
	if scanner.Err() != nil {
		return nil
	}
	return records
}

// applyRunSpan extracts run span metadata (start time, pid, duration) into tel.
func applyRunSpan(rec telemetry.Record, ends map[string]telemetry.Record, tel *telemetryData) {
	if ts, err := time.Parse(time.RFC3339, rec.StartTS); err == nil {
		tel.RunStartTS = ts
	}
	if pidVal, ok := rec.Attrs["pid"]; ok {
		tel.RunSpanPID = extractPID(pidVal)
	}
	if end, ok := ends[rec.SpanID]; ok {
		tel.RunDurationMS = end.DurationMS
	}
}

func extractPID(v any) *int {
	switch val := v.(type) {
	case float64:
		pid := int(val)
		return &pid
	case int:
		pid := val
		return &pid
	}
	return nil
}

func issueFromRunID(runID string) int {
	// Format: "issue-<N>-<timestamp>"
	parts := strings.SplitN(runID, "-", 3)
	if len(parts) >= 2 && parts[0] == "issue" {
		n, err := strconv.Atoi(parts[1])
		if err == nil {
			return n
		}
	}
	return 0
}

// timestampSuffix extracts the UTC timestamp suffix from a run ID for sorting.
func timestampSuffix(runID string) string {
	idx := strings.LastIndex(runID, "-")
	if idx < 0 {
		return runID
	}
	return runID[idx+1:]
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}
