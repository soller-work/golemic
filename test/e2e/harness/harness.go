//go:build e2e

// Package harness provides the build and run infrastructure for the deterministic
// E2E test suite. It builds the golemic binary and the detagent (fake pi), runs
// golemic against the golemic_e2e sandbox, and handles idempotent cleanup.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Harness holds the binaries and sandbox configuration for one E2E test run.
type Harness struct {
	E2EPath    string // local path to golemic_e2e sandbox
	E2ERepo    string // GitHub owner/repo slug for the sandbox
	GolemicBin string // path to built golemic binary
	PiDir      string // temp dir containing the fake pi binary
	GolemicDir string // <HOME>/.golemic/<project>

	project       string // sandbox project name (for per-run isolation)
	devToken      string
	reviewerToken string
	homeDir       string
}

// RunResult holds the captured output and exit code from a golemic subprocess.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// New creates a Harness. Returns (nil, reason) and calls t.Skip when prerequisites
// are not met so the test skips rather than failing.
func New(t *testing.T) *Harness {
	t.Helper()

	e2ePath := resolveE2EPath()
	if e2ePath == "" {
		t.Skip("golemic_e2e sandbox not found — set GOLEMIC_E2E_PATH or check out ~/golemic_e2e")
		return nil
	}

	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh CLI not in PATH — skipping E2E test")
		return nil
	}

	e2eRepo := resolveE2ERepo(t)
	if e2eRepo == "" {
		return nil // t.Skip already called
	}

	if out, err := exec.Command("gh", "repo", "view", e2eRepo, "--json", "name").CombinedOutput(); err != nil {
		t.Skipf("golemic_e2e GitHub repo %q not accessible: %v\n%s", e2eRepo, err, out)
		return nil
	}

	devToken := os.Getenv("GOLEMIC_DEV_TOKEN")
	reviewerToken := os.Getenv("GOLEMIC_REVIEWER_TOKEN")
	if devToken == "" || reviewerToken == "" {
		t.Skip("GOLEMIC_DEV_TOKEN and/or GOLEMIC_REVIEWER_TOKEN not set — skipping E2E test")
		return nil
	}

	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		if d, err := os.UserHomeDir(); err == nil {
			homeDir = d
		}
	}

	srcRoot := findSourceRoot()
	if srcRoot == "" {
		t.Skip("cannot locate golemic source root (go.mod not found)")
		return nil
	}

	golemicBin, piDir, err := buildBinaries(t, srcRoot)
	if err != nil {
		t.Skipf("failed to build binaries: %v", err)
		return nil
	}

	project := resolveProject(e2ePath)
	if project == "" {
		t.Skip("cannot read .golemic/config.json project name from sandbox")
		return nil
	}

	h := &Harness{
		E2EPath:       e2ePath,
		E2ERepo:       e2eRepo,
		GolemicBin:    golemicBin,
		PiDir:         piDir,
		GolemicDir:    filepath.Join(homeDir, ".golemic", project),
		project:       project,
		devToken:      devToken,
		reviewerToken: reviewerToken,
		homeDir:       homeDir,
	}

	if err := ensureRequiredLabels(t, e2eRepo, devToken); err != nil {
		t.Skipf("cannot provision required labels in %s: %v", e2eRepo, err)
		return nil
	}

	return h
}

// IsolateRun creates a per-run harness with its own temporary HOME directory and
// its own linked sandbox worktree so concurrent scenario runs share no mutable
// local state. The per-run HOME gives golemic an isolated state dir
// (<HOME>/.golemic/<project>) that contains exactly one run after golemic exits,
// making LatestRunEventsPath unambiguous. Cleanup is registered with t.Cleanup:
// it removes the temp HOME tree and prunes dangling git worktree metadata.
func (h *Harness) IsolateRun(t *testing.T) *Harness {
	t.Helper()

	tmpHome, err := os.MkdirTemp("", "e2e-home-*")
	if err != nil {
		t.Fatalf("IsolateRun: create temp home: %v", err)
	}

	sandboxPath := filepath.Join(tmpHome, "sandbox")
	if err := runInDir(h.E2EPath, "git", "worktree", "add", "--detach", sandboxPath, "HEAD"); err != nil {
		os.RemoveAll(tmpHome)
		t.Fatalf("IsolateRun: git worktree add: %v", err)
	}

	t.Cleanup(func() {
		os.RemoveAll(tmpHome)
		// Prune dangling references for the sandbox worktree and any golemic
		// worktrees that were created inside this run's state dir.
		runInDir(h.E2EPath, "git", "worktree", "prune") //nolint:errcheck
	})

	return &Harness{
		E2EPath:       sandboxPath,
		E2ERepo:       h.E2ERepo,
		GolemicBin:    h.GolemicBin,
		PiDir:         h.PiDir,
		GolemicDir:    filepath.Join(tmpHome, ".golemic", h.project),
		project:       h.project,
		devToken:      h.devToken,
		reviewerToken: h.reviewerToken,
		homeDir:       tmpHome,
	}
}

// CreateIssue creates a sandbox issue with the given scenario marker and returns
// the issue number. The body contains the machine-readable E2E-SCENARIO line
// plus a human-readable description.
func (h *Harness) CreateIssue(scenario string) (int, error) {
	title := fmt.Sprintf("E2E %s %d", scenario, time.Now().UnixNano())
	body := fmt.Sprintf("E2E-SCENARIO: %s\n\nAutomated E2E test issue — safe to close.", scenario)
	return ghCreateIssue(h.E2ERepo, title, body)
}

// CloseIssue closes a sandbox issue. Safe to call multiple times (idempotent).
func (h *Harness) CloseIssue(issueNum int) {
	cmd := exec.Command("gh", "issue", "close",
		fmt.Sprintf("%d", issueNum), "--repo", h.E2ERepo, "--reason", "not planned")
	cmd.CombinedOutput() //nolint:errcheck
}

// CreateCollisionPR creates an open PR for the given issue's branch so golemic
// detects a collision when it runs. Returns the PR number and cleanup function.
func (h *Harness) CreateCollisionPR(t *testing.T, issueNum int) (prNum int, cleanup func()) {
	t.Helper()
	branch := fmt.Sprintf("golemic/issue-%d", issueNum)
	uniqueBranch := fmt.Sprintf("e2e-collision-%d-%d", issueNum, time.Now().UnixNano())

	// Create a temp worktree in the sandbox to push a branch for the PR.
	wtDir := filepath.Join(h.E2EPath, ".e2e-tmp", uniqueBranch)
	if err := runInDir(h.E2EPath, "git", "worktree", "add", "--detach", wtDir, "HEAD"); err != nil {
		t.Skipf("cannot create git worktree for collision setup: %v", err)
		return 0, func() {}
	}

	cleanup = func() {
		runInDir(h.E2EPath, "git", "worktree", "remove", "--force", wtDir) //nolint:errcheck
		runInDir(h.E2EPath, "git", "push", "origin", "--delete", branch)   //nolint:errcheck
	}

	if err := runInDir(wtDir, "git", "checkout", "-b", branch); err != nil {
		cleanup()
		t.Skipf("cannot create collision branch %s: %v", branch, err)
		return 0, func() {}
	}
	if err := runInDir(wtDir, "git", "commit", "--allow-empty", "-m", "chore: e2e collision setup"); err != nil {
		cleanup()
		t.Skipf("cannot commit for collision PR: %v", err)
		return 0, func() {}
	}
	if err := runInDir(wtDir, "git", "push", "origin", branch); err != nil {
		cleanup()
		t.Skipf("cannot push collision branch: %v", err)
		return 0, func() {}
	}

	out, err := runGhOut("pr", "create",
		"--repo", h.E2ERepo,
		"--head", branch,
		"--base", "main",
		"--title", fmt.Sprintf("E2E collision setup for issue #%d", issueNum),
		"--body", "Automated collision test — safe to close.")
	if err != nil {
		cleanup()
		t.Skipf("cannot create collision PR: %v", err)
		return 0, func() {}
	}
	prNum = parsePRNum(out)

	closePR := func() {
		if prNum > 0 {
			runGhOut("pr", "close", fmt.Sprintf("%d", prNum), "--repo", h.E2ERepo) //nolint:errcheck
		}
	}
	return prNum, func() {
		closePR()
		cleanup()
	}
}

// Run spawns golemic against the sandbox for the given issue number and waits
// for it to finish. noClean skips --clean (required for collision detection).
// extraEnv values are appended to the subprocess environment (format: "KEY=VALUE").
//
// Token values in the captured output are redacted before returning.
func (h *Harness) Run(ctx context.Context, issueNum int, noClean bool, extraEnv ...string) *RunResult {
	args := []string{"run", "--issue", fmt.Sprintf("%d", issueNum)}
	if !noClean {
		args = append(args, "--clean")
	}
	cmd := exec.CommandContext(ctx, h.GolemicBin, args...)
	cmd.Dir = h.E2EPath

	// Build environment: inherit current env, inject tokens and fake pi.
	env := filterEnv(os.Environ(), "GH_TOKEN", "GOLEMIC_DEV_TOKEN", "GOLEMIC_REVIEWER_TOKEN")
	env = append(env,
		"HOME="+h.homeDir,
		"GOLEMIC_DEV_TOKEN="+h.devToken,
		"GOLEMIC_REVIEWER_TOKEN="+h.reviewerToken,
		// Prepend the fake pi directory first on PATH.
		"PATH="+h.PiDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	env = append(env, extraEnv...)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}

	stdoutStr := redact(stdout.String(), h.devToken, h.reviewerToken)
	stderrStr := redact(stderr.String(), h.devToken, h.reviewerToken)
	return &RunResult{
		Stdout:   stdoutStr,
		Stderr:   stderrStr,
		ExitCode: exitCode,
	}
}

// RunWithTimeout wraps Run with a context deadline. timeoutSec controls the
// wall-clock limit; zero means 30 minutes. noClean skips --clean (required for
// collision detection).
func (h *Harness) RunWithTimeout(t *testing.T, issueNum, timeoutSec int, noClean bool, extraEnv ...string) *RunResult {
	t.Helper()
	d := time.Duration(timeoutSec) * time.Second
	if d <= 0 {
		d = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return h.Run(ctx, issueNum, noClean, extraEnv...)
}

// RemoveWorktrees removes all worktrees created for the sandbox's golemic state dir.
func (h *Harness) RemoveWorktrees() error {
	wtDir := filepath.Join(h.GolemicDir, "worktrees")
	entries, err := os.ReadDir(wtDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("readdir %s: %w", wtDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wtPath := filepath.Join(wtDir, e.Name())
		if runErr := runInDir(h.E2EPath, "git", "worktree", "remove", "--force", wtPath); runErr != nil {
			if rmErr := os.RemoveAll(wtPath); rmErr != nil {
				return fmt.Errorf("remove worktree %s: git=%v rm=%w", wtPath, runErr, rmErr)
			}
		}
	}
	return nil
}

// RemoveRuns removes all run directories from the sandbox's golemic state dir.
func (h *Harness) RemoveRuns() error {
	runsDir := filepath.Join(h.GolemicDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("readdir %s: %w", runsDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(runsDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// DeleteBranch deletes a remote branch from the sandbox. Idempotent.
func (h *Harness) DeleteBranch(branch string) {
	runInDir(h.E2EPath, "git", "push", "origin", "--delete", branch) //nolint:errcheck
}

// ClosePR closes a PR. Idempotent.
func (h *Harness) ClosePR(prNum int) {
	if prNum > 0 {
		runGhOut("pr", "close", fmt.Sprintf("%d", prNum), "--repo", h.E2ERepo) //nolint:errcheck
	}
}

// RunFinishedOutcome reads events.jsonl at path and returns the run_finished
// outcome string (e.g. "success", "dev_failed", "aborted", "timeout", "stalled").
// Returns "" if the event is not found.
func RunFinishedOutcome(eventsPath string) string {
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "run_finished" {
			continue
		}
		var p struct {
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil {
			return p.Outcome
		}
	}
	return ""
}

// HasEvent returns true if events.jsonl contains at least one event with the given type.
func HasEvent(eventsPath, eventType string) bool {
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == eventType {
			return true
		}
	}
	return false
}

// CountEvents returns the number of events with the given type in events.jsonl.
func CountEvents(eventsPath, eventType string) int {
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == eventType {
			count++
		}
	}
	return count
}

// PRNumFromEvents reads events.jsonl and returns the PR number from pr_opened.
func PRNumFromEvents(eventsPath string) int {
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type != "pr_opened" {
			continue
		}
		var p struct {
			PRNumber string `json:"prNumber"`
		}
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		n, err := strconv.Atoi(p.PRNumber)
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// LatestRunEventsPath returns the path to events.jsonl for the latest run in
// the golemic state directory. Returns "" if no runs exist.
func (h *Harness) LatestRunEventsPath() string {
	runsDir := filepath.Join(h.GolemicDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return ""
	}
	// Runs are named with timestamps; sort order finds the latest.
	latest := ""
	for _, e := range entries {
		if e.IsDir() && e.Name() > latest {
			latest = e.Name()
		}
	}
	if latest == "" {
		return ""
	}
	return filepath.Join(runsDir, latest, "events.jsonl")
}

// requiredSandboxLabels mirrors internal/preflight/preflight.go:requiredLabels.
// Update both when golemic's workflow label requirements change.
var requiredSandboxLabels = []struct {
	name        string
	color       string
	description string
}{
	{"in-progress", "fbca04", "Issue is currently claimed by an autonomous runner"},
	{"needs-human", "d93f0b", "Autonomous runner failed; requires human triage"},
	{"confidence:high", "0075ca", "Reviewer confidence: high"},
	{"confidence:medium", "e4e669", "Reviewer confidence: medium"},
	{"confidence:low", "d93f0b", "Reviewer confidence: low"},
}

// ensureRequiredLabels idempotently creates the workflow labels golemic's
// preflight requires in the sandbox repo. Uses --force so already-existing
// labels are updated rather than causing an error.
func ensureRequiredLabels(t *testing.T, repo, token string) error {
	t.Helper()
	env := append(os.Environ(), "GH_TOKEN="+token)
	for _, lbl := range requiredSandboxLabels {
		cmd := exec.Command("gh", "label", "create", lbl.name,
			"--repo", repo,
			"--color", lbl.color,
			"--description", lbl.description,
			"--force")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("gh label create %s: %w\n%s", lbl.name, err, out)
		}
	}
	return nil
}

// --- helpers ---

func resolveE2EPath() string {
	if p := os.Getenv("GOLEMIC_E2E_PATH"); p != "" {
		if isGitDir(p) {
			return p
		}
	}
	home := os.Getenv("HOME")
	for _, p := range []string{
		filepath.Join(home, "golemic_e2e"),
		filepath.Join(home, "Dev", "golemic_e2e"),
	} {
		if isGitDir(p) {
			return p
		}
	}
	return ""
}

func isGitDir(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func resolveE2ERepo(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("GOLEMIC_E2E_REPO"); r != "" {
		return r
	}
	out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		t.Skipf("cannot determine GitHub owner (gh not authenticated?): %v", err)
		return ""
	}
	owner := strings.TrimSpace(string(out))
	if owner == "" {
		t.Skip("gh api user returned empty login — skipping E2E test")
		return ""
	}
	return owner + "/golemic_e2e"
}

func resolveProject(e2ePath string) string {
	cfgPath := filepath.Join(e2ePath, ".golemic", "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	var cfg struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Project
}

func findSourceRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func buildBinaries(t *testing.T, srcRoot string) (golemicBin, piDir string, err error) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "e2e-bins-*")
	if err != nil {
		return "", "", fmt.Errorf("mkdirtemp: %w", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	// Build golemic binary (or use GOLEMIC_BINARY override).
	golemicBin = os.Getenv("GOLEMIC_BINARY")
	if golemicBin == "" {
		golemicBin = filepath.Join(tmpDir, "golemic")
		cmd := exec.Command("go", "build", "-o", golemicBin, "./cmd/golemic")
		cmd.Dir = srcRoot
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			return "", "", fmt.Errorf("go build ./cmd/golemic: %w\n%s", buildErr, out)
		}
	}

	// Build detagent as "pi" into a dedicated directory.
	piDir = filepath.Join(tmpDir, "pidir")
	if err := os.MkdirAll(piDir, 0755); err != nil {
		return "", "", fmt.Errorf("mkdir pidir: %w", err)
	}
	piOut := filepath.Join(piDir, "pi")
	cmd := exec.Command("go", "build", "-o", piOut, "./test/e2e/detagent")
	cmd.Dir = srcRoot
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		return "", "", fmt.Errorf("go build ./test/e2e/detagent: %w\n%s", buildErr, out)
	}

	return golemicBin, piDir, nil
}

func ghCreateIssue(repo, title, body string) (int, error) {
	out, err := runGhOut("issue", "create",
		"--repo", repo,
		"--title", title,
		"--body", body,
	)
	if err != nil {
		return 0, fmt.Errorf("gh issue create: %w", err)
	}
	// Output is the issue URL; parse the number from the last segment.
	url := strings.TrimSpace(out)
	idx := strings.LastIndex(url, "/")
	if idx < 0 {
		return 0, fmt.Errorf("unexpected gh issue create output: %s", out)
	}
	n, err := strconv.Atoi(url[idx+1:])
	if err != nil {
		return 0, fmt.Errorf("parse issue number from %q: %w", url, err)
	}
	return n, nil
}

func runGhOut(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return "", fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

func runInDir(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

var prNumRe = regexp.MustCompile(`/(\d+)$`)

func parsePRNum(url string) int {
	m := prNumRe.FindStringSubmatch(strings.TrimSpace(url))
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func filterEnv(env []string, keys ...string) []string {
	var out []string
	for _, e := range env {
		skip := false
		for _, k := range keys {
			if strings.HasPrefix(e, k+"=") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, e)
		}
	}
	return out
}

func redact(s, devToken, reviewerToken string) string {
	if devToken != "" {
		s = strings.ReplaceAll(s, devToken, "***REDACTED***")
	}
	if reviewerToken != "" && reviewerToken != devToken {
		s = strings.ReplaceAll(s, reviewerToken, "***REDACTED***")
	}
	return s
}
