package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/eventlog"
	"golemic/internal/preflight"
)

// ---------------------------------------------------------------------------
// Shared runner test fixture
// ---------------------------------------------------------------------------

// runnerFixture holds a fully wired Runner and its associated test resources.
// eventLogPath is always computed; the directory and seeded events exist only
// when withSeedEventLog is passed to newRunnerFixture.
type runnerFixture struct {
	r            *Runner
	homeDir      string // resolved home dir (may be /tmp when withShortHome is used)
	repoRoot     string
	project      string
	runID        string
	eventLogPath string // filepath.Join(homeDir, ".golemic", project, "runs", runID, "events.jsonl")
	stdout       *bytes.Buffer
	stderr       *bytes.Buffer
}

type fixtureOpt func(*fixtureCfg)

type fixtureCfg struct {
	executor       preflight.Executor
	shortProject   string // if non-empty, use /tmp as homeDir with this project name
	shortRunID     string // run ID used when shortProject is set
	issueNum       int    // default 42
	runID          string
	branchName     string
	cfg            *config.Config
	cfgJSON        string // raw JSON written to repoRoot/.golemic/config.json; overrides default
	issue          *issueData
	guidelines     []string // role names; creates .golemic/guidelines/<role>.md
	agents         []string // role names; creates .golemic/agents/<role>.md
	seedEventLog   bool     // create event log dir and seed with run_started
	ciPollInterval time.Duration
	ciTimeout      time.Duration
	preflighter    Preflighter
	noopPrecheck   bool // inject no-op reviewerPrecheckFn
	turnCounter    int
	createDevWT    bool // create homeDir/.golemic/<project>/worktrees/issue-<n>
}

func withExecutor(exec preflight.Executor) fixtureOpt {
	return func(c *fixtureCfg) { c.executor = exec }
}

// withShortHome sets the runner's homeDir to /tmp with a short project name so
// unix socket paths stay within the 104-byte limit on macOS. Credentials are
// copied from the base homeDir (from setupRunnerTest) to /tmp/.golemic/<proj>.
func withShortHome(shortProject, shortRunID string) fixtureOpt {
	return func(c *fixtureCfg) {
		c.shortProject = shortProject
		c.shortRunID = shortRunID
	}
}

func withIssueNum(n int) fixtureOpt {
	return func(c *fixtureCfg) { c.issueNum = n }
}

func withRunID(id string) fixtureOpt {
	return func(c *fixtureCfg) { c.runID = id }
}

func withBranchName(b string) fixtureOpt {
	return func(c *fixtureCfg) { c.branchName = b }
}

func withConfig(cfg *config.Config) fixtureOpt {
	return func(c *fixtureCfg) { c.cfg = cfg }
}

// withConfigJSON writes raw JSON to repoRoot/.golemic/config.json. Use when the
// config file content must differ from what withShortHome generates by default.
func withConfigJSON(j string) fixtureOpt {
	return func(c *fixtureCfg) { c.cfgJSON = j }
}

func withIssue(i *issueData) fixtureOpt {
	return func(c *fixtureCfg) { c.issue = i }
}

// withGuidelines creates .golemic/guidelines/<role>.md for each named role.
func withGuidelines(roles ...string) fixtureOpt {
	return func(c *fixtureCfg) { c.guidelines = roles }
}

// withAgents creates .golemic/agents/<role>.md for each named role.
func withAgents(roles ...string) fixtureOpt {
	return func(c *fixtureCfg) { c.agents = roles }
}

// withSeedEventLog creates the event log directory and writes a run_started event.
func withSeedEventLog() fixtureOpt {
	return func(c *fixtureCfg) { c.seedEventLog = true }
}

func withCIPollInterval(d time.Duration) fixtureOpt {
	return func(c *fixtureCfg) { c.ciPollInterval = d }
}

func withCITimeout(d time.Duration) fixtureOpt {
	return func(c *fixtureCfg) { c.ciTimeout = d }
}

func withPreflighter(p Preflighter) fixtureOpt {
	return func(c *fixtureCfg) { c.preflighter = p }
}

// withNoopReviewerPrecheck injects a no-op reviewerPrecheckFn so tests do not
// need a real git repository in the reviewer worktree.
func withNoopReviewerPrecheck() fixtureOpt {
	return func(c *fixtureCfg) { c.noopPrecheck = true }
}

func withTurnCounter(n int) fixtureOpt {
	return func(c *fixtureCfg) { c.turnCounter = n }
}

// withDevWorktreeDir creates homeDir/.golemic/<project>/worktrees/issue-<n>
// so the runner can find the dev worktree without a real git operation.
func withDevWorktreeDir() fixtureOpt {
	return func(c *fixtureCfg) { c.createDevWT = true }
}

// newRunnerFixture assembles a Runner with credentials, optional guideline and
// agent files, and optional event-log seeding. All existing per-file runner
// builder helpers delegate to this function; no test behaviour changes.
func newRunnerFixture(t *testing.T, opts ...fixtureOpt) runnerFixture {
	t.Helper()

	baseHomeDir, repoRoot, baseProject := setupRunnerTest(t)
	fc := &fixtureCfg{issueNum: 42}
	for _, opt := range opts {
		opt(fc)
	}

	homeDir, project := resolveFixtureHome(fc, baseHomeDir, baseProject, t)
	runID := resolveFixtureRunID(fc)
	branchName := resolveFixtureBranchName(fc)
	writeFixtureConfig(t, repoRoot, project, fc)
	creds := loadFixtureCreds(t, baseHomeDir, baseProject, homeDir, project)
	writeFixtureFiles(t, repoRoot, fc)
	r := buildFixtureRunner(fc, homeDir, repoRoot, project, runID, branchName, creds)
	stdout, stderr := attachFixtureBuffers(r)
	createFixtureDevWorktree(t, fc, homeDir, project)
	eventLogPath := filepath.Join(homeDir, ".golemic", project, "runs", runID, "events.jsonl")
	if fc.seedEventLog {
		seedFixtureEventLog(t, eventLogPath, fc.issueNum, runID)
	}
	return runnerFixture{
		r:            r,
		homeDir:      homeDir,
		repoRoot:     repoRoot,
		project:      project,
		runID:        runID,
		eventLogPath: eventLogPath,
		stdout:       stdout,
		stderr:       stderr,
	}
}

func resolveFixtureHome(fc *fixtureCfg, baseHomeDir, baseProject string, t *testing.T) (string, string) {
	homeDir := baseHomeDir
	project := baseProject
	if fc.shortProject == "" {
		return homeDir, project
	}
	homeDir = "/tmp"
	project = fc.shortProject
	t.Cleanup(func() { os.RemoveAll(filepath.Join("/tmp", ".golemic", fc.shortProject)) }) //nolint:errcheck
	return homeDir, project
}

func resolveFixtureRunID(fc *fixtureCfg) string {
	switch {
	case fc.runID != "":
		return fc.runID
	case fc.shortRunID != "":
		return fc.shortRunID
	default:
		return fmt.Sprintf("issue-%d-test", fc.issueNum)
	}
}

func resolveFixtureBranchName(fc *fixtureCfg) string {
	if fc.branchName != "" {
		return fc.branchName
	}
	return fmt.Sprintf("golemic/issue-%d", fc.issueNum)
}

func writeFixtureConfig(t *testing.T, repoRoot, project string, fc *fixtureCfg) {
	if fc.shortProject == "" && fc.cfgJSON == "" {
		return
	}
	j := fc.cfgJSON
	if j == "" {
		j = fmt.Sprintf(`{"project":%q,"verify_command":"go test"}`, project)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".golemic", "config.json"), []byte(j), 0644); err != nil {
		t.Fatal(err)
	}
}

func loadFixtureCreds(t *testing.T, baseHomeDir, baseProject, homeDir, project string) *credentials.Credentials {
	t.Helper()
	baseCreds, err := credentials.NewLoader(baseHomeDir).Load(baseProject)
	if err != nil {
		t.Fatalf("load base credentials: %v", err)
	}
	if homeDir == baseHomeDir {
		return baseCreds
	}
	credDir := filepath.Join(homeDir, ".golemic", project)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	credJSON := fmt.Sprintf(`{"dev_token":%q,"reviewer_token":%q}`, baseCreds.DevToken(), baseCreds.ReviewerToken())
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(credJSON), 0600); err != nil {
		t.Fatal(err)
	}
	creds, err := credentials.NewLoader(homeDir).Load(project)
	if err != nil {
		t.Fatalf("load short-home credentials: %v", err)
	}
	return creds
}

func writeFixtureFiles(t *testing.T, repoRoot string, fc *fixtureCfg) {
	t.Helper()
	writeFixtureRoleFiles(t, filepath.Join(repoRoot, ".golemic", "guidelines"), fc.guidelines, []byte("# Guidelines"))
	writeFixtureRoleFiles(t, filepath.Join(repoRoot, ".golemic", "agents"), fc.agents, []byte("---\nmodel: test/model\n---\npersona body\n"))
}

func writeFixtureRoleFiles(t *testing.T, dir string, roles []string, content []byte) {
	if len(roles) == 0 {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		if err := os.WriteFile(filepath.Join(dir, role+".md"), content, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func buildFixtureRunner(fc *fixtureCfg, homeDir, repoRoot, project, runID, branchName string, creds *credentials.Credentials) *Runner {
	r := New(fc.executor, homeDir, repoRoot, fc.issueNum)
	r.repoRoot = repoRoot
	r.project = project
	r.homeDir = homeDir
	r.runID = runID
	r.branchName = branchName
	r.creds = creds
	if fc.cfg != nil {
		r.cfg = fc.cfg
	} else {
		r.cfg = &config.Config{VerifyCommand: "go test"}
	}
	if fc.issue != nil {
		r.issue = fc.issue
	} else {
		r.issue = &issueData{Number: fc.issueNum, Title: "Test Issue", State: "OPEN"}
	}
	if fc.turnCounter > 0 {
		r.turnCounter = fc.turnCounter
	}
	if fc.ciPollInterval > 0 {
		r.SetCIPollInterval(fc.ciPollInterval)
	}
	if fc.ciTimeout > 0 {
		r.SetCITimeout(fc.ciTimeout)
	}
	if fc.preflighter != nil {
		r.SetPreflighter(fc.preflighter)
	}
	if fc.noopPrecheck {
		r.reviewerPrecheckFn = func(_, _ string) (string, error) { return "", nil }
	}
	return r
}

func attachFixtureBuffers(r *Runner) (*bytes.Buffer, *bytes.Buffer) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	r.SetStdout(stdout)
	r.SetStderr(stderr)
	return stdout, stderr
}

func createFixtureDevWorktree(t *testing.T, fc *fixtureCfg, homeDir, project string) {
	if !fc.createDevWT {
		return
	}
	devWT := filepath.Join(homeDir, ".golemic", project, "worktrees", fmt.Sprintf("issue-%d", fc.issueNum))
	if err := os.MkdirAll(devWT, 0755); err != nil {
		t.Fatal(err)
	}
}

func seedFixtureEventLog(t *testing.T, eventLogPath string, issueNum int, runID string) {
	if err := os.MkdirAll(filepath.Dir(eventLogPath), 0755); err != nil {
		t.Fatal(err)
	}
	w, err := eventlog.NewWriter(eventLogPath)
	if err != nil {
		t.Fatalf("seed event log: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"issue": issueNum, "runId": runID})
	_ = w.Write(eventlog.Event{Type: eventlog.EventRunStarted, Ts: time.Now().Format(time.RFC3339), RunID: runID, Payload: payload})
	w.Close() //nolint:errcheck
}
