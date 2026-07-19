// Package preflight checks all prerequisites for running golemic.
// All external commands (gh, pi, git) are behind the injectable Executor interface.
package preflight

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golemic/internal/config"
	"golemic/internal/credentials"
)

//go:embed templates/guidelines/dev.md templates/guidelines/reviewer.md
var templateFS embed.FS

// Result holds the outcome of a single check.
type Result struct {
	Name    string
	Ok      bool
	Details string // human-readable detail when !Ok
}

// Results is a slice of check results.
type Results []Result

// AllOK returns true if every check passed.
func (r Results) AllOK() bool {
	for _, res := range r {
		if !res.Ok {
			return false
		}
	}
	return true
}

// ErrExit is returned by the Executor when a command exits with a non-zero code.
type ErrExit struct {
	ExitCode int
	Stderr   string
}

func (e *ErrExit) Error() string {
	msg := fmt.Sprintf("exit code %d", e.ExitCode)
	if e.Stderr != "" {
		msg += ": " + strings.TrimSpace(e.Stderr)
	}
	return msg
}

// Executor runs external commands. The current implementation (osExecutor) is
// single-threaded; callers that need concurrency must add their own synchronisation.
type Executor interface {
	// Run executes a command and returns stdout on success, or an error on failure.
	Run(name string, args ...string) (string, error)
	// RunWithEnv executes a command with additional environment variables set.
	RunWithEnv(env map[string]string, name string, args ...string) (string, error)
	// RunInDir executes a command with the given working directory.
	RunInDir(dir string, name string, args ...string) (string, error)
	// RunWithEnvInDir executes a command with the given working directory and environment.
	RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error)
}

// Preflight runs all prerequisite checks in fixed order.
type Preflight struct {
	executor   Executor
	homeDir    string
	repoRoot   string
	stdout     io.Writer
	cachedCfg  *config.Config // cached config loaded by checkConfig, reused by checkCredentials
	configDone bool           // true once checkConfig has attempted to load
	checkMode  bool           // true = read-only check mode (no writes, local token comparison)
	lookupEnv  func(string) (string, bool)
}

// New creates a new Preflight checker.
// executor is used for all external commands, homeDir is the user's home directory
// (~/.golemic is resolved relative to it), repoRoot is the git repository root.
func New(executor Executor, homeDir, repoRoot string) *Preflight {
	return &Preflight{
		executor: executor,
		homeDir:  homeDir,
		repoRoot: repoRoot,
		stdout:   io.Discard,
	}
}

// SetStdout sets the writer for check result lines. Defaults to io.Discard.
func (p *Preflight) SetStdout(w io.Writer) {
	p.stdout = w
}

// SetLookupEnv injects a custom env lookup for credentials loading.
// nil means os.LookupEnv (production default).
func (p *Preflight) SetLookupEnv(fn func(string) (string, bool)) {
	p.lookupEnv = fn
}

// ghUserLogin holds the login field from `gh api user`.
type ghUserLogin struct {
	Login string `json:"login"`
}

// RunAll runs all checks in setup mode (may scaffold files) and returns results.
// Output format: OK: <name> / FAILED: <name> - <detail>, final summary ok/failed.
func (p *Preflight) RunAll() Results {
	p.checkMode = false
	return p.runChecks()
}

// Check runs all checks in read-only mode: no file writes, no scaffolding,
// token distinctness via local value comparison (no gh api call).
// Output format: OK: <name> / FAILED: <name> - <detail>, final summary ok/failed.
func (p *Preflight) Check() Results {
	p.checkMode = true
	return p.runChecks()
}

// runChecks executes the 7 checks in fixed order, prints per-check lines and
// the final ok/failed summary to p.stdout, and returns the results.
func (p *Preflight) runChecks() Results { //nolint:errcheck // writes to user-controlled stdout; ignoring error is intentional
	// Reset cached state so repeated calls on the same instance are independent.
	p.cachedCfg = nil
	p.configDone = false

	results := Results{
		p.checkGhVersion(),
		p.checkPiVersion(),
		p.checkGit(),
		p.checkScaffolding(),
		p.checkConfig(),
		p.checkCredentials(),
		p.checkLabels(),
	}

	for _, r := range results {
		if r.Ok {
			_, _ = fmt.Fprintf(p.stdout, "OK: %s\n", r.Name)
		} else {
			_, _ = fmt.Fprintf(p.stdout, "FAILED: %s - %s\n", r.Name, r.Details)
		}
	}

	if results.AllOK() {
		_, _ = fmt.Fprintln(p.stdout, "ok")
	} else {
		_, _ = fmt.Fprintln(p.stdout, "failed")
	}

	return results
}

// checkGhVersion checks that `gh --version` succeeds.
func (p *Preflight) checkGhVersion() Result {
	_, err := p.executor.Run("gh", "--version")
	if err != nil {
		var ee *ErrExit
		if errors.As(err, &ee) {
			return Result{Name: "gh installiert", Ok: false, Details: "gh --version exited with code " + fmt.Sprint(ee.ExitCode)}
		}
		return Result{Name: "gh installiert", Ok: false, Details: "gh not found: " + err.Error()}
	}
	return Result{Name: "gh installiert", Ok: true}
}

// checkPiVersion checks that `pi --version` succeeds.
func (p *Preflight) checkPiVersion() Result {
	_, err := p.executor.Run("pi", "--version")
	if err != nil {
		var ee *ErrExit
		if errors.As(err, &ee) {
			return Result{Name: "pi installiert", Ok: false, Details: "pi --version exited with code " + fmt.Sprint(ee.ExitCode)}
		}
		return Result{Name: "pi installiert", Ok: false, Details: "pi not found: " + err.Error()}
	}
	return Result{Name: "pi installiert", Ok: true}
}

// checkGit performs git-related checks: version, worktree support, repo context,
// remote origin with HTTPS URL.
func (p *Preflight) checkGit() Result {
	// 1. git --version
	_, err := p.executor.Run("git", "--version")
	if err != nil {
		var ee *ErrExit
		if errors.As(err, &ee) {
			return Result{Name: "git", Ok: false, Details: "git --version exited with code " + fmt.Sprint(ee.ExitCode)}
		}
		return Result{Name: "git", Ok: false, Details: "git not found: " + err.Error()}
	}

	// 2. git worktree list (also verifies we are inside a git repo)
	_, err = p.executor.RunInDir(p.repoRoot, "git", "worktree", "list")
	if err != nil {
		return Result{Name: "git", Ok: false, Details: "git worktree list failed: " + err.Error()}
	}

	// 3. Check remote origin exists
	remoteOut, err := p.executor.RunInDir(p.repoRoot, "git", "config", "--get", "remote.origin.url")
	if err != nil {
		return Result{Name: "git", Ok: false, Details: "no remote 'origin' configured: " + err.Error()}
	}
	remoteURL := strings.TrimSpace(remoteOut)
	if remoteURL == "" {
		return Result{Name: "git", Ok: false, Details: "remote 'origin' has empty URL"}
	}

	// 4. Check that remote URL is HTTPS (not SSH)
	if isSSHURL(remoteURL) {
		return Result{Name: "git", Ok: false, Details: "remote 'origin' URL must be HTTPS, got SSH-style URL: " + maskURL(remoteURL)}
	}
	if !strings.HasPrefix(remoteURL, "https://") {
		return Result{Name: "git", Ok: false, Details: "remote 'origin' URL must be HTTPS, got: " + maskURL(remoteURL)}
	}

	return Result{Name: "git", Ok: true}
}

// isSSHURL returns true if the URL looks like an SSH remote URL.
func isSSHURL(url string) bool {
	return strings.HasPrefix(url, "git@") ||
		strings.HasPrefix(url, "ssh://") ||
		strings.HasPrefix(url, "git://") ||
		strings.HasPrefix(url, "git+ssh://")
}

// maskURL replaces the credential part of a URL (if any) with *** for safe display.
func maskURL(url string) string {
	// Only mask if it looks like https://user:pass@host/path
	if !strings.HasPrefix(url, "https://") {
		return url
	}
	rest := strings.TrimPrefix(url, "https://")
	atIdx := strings.Index(rest, "@")
	if atIdx < 0 {
		return url
	}
	return "https://***@" + rest[atIdx+1:]
}

// writeFileAtomic creates a file with the given content and permissions.
// It creates parent directories with 0755 if they don't exist.
// Returns fs.ErrExist if the file already exists (idempotency).
func writeFileAtomic(path string, content []byte, perms os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Idempotency: never overwrite existing file
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("file %s already exists: %w", path, fs.ErrExist)
	}

	// Write to temp file, then rename for atomicity
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, content, perms); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("rename %s: %w", path, err)
	}

	return nil
}

// checkScaffolding checks if .golemic/ exists.
// In setup mode: creates scaffolding from templates if missing.
// In check mode: missing config.json → FAILED, nothing written.
func (p *Preflight) checkScaffolding() Result {
	golemicDir := filepath.Join(p.repoRoot, ".golemic")
	configPath := filepath.Join(golemicDir, "config.json")

	// Check if config.json already exists
	if _, err := os.Stat(configPath); err == nil {
		return Result{Name: ".golemic/ Scaffolding", Ok: true}
	}

	// Check mode: never write, missing → FAILED
	if p.checkMode {
		return Result{Name: ".golemic/ Scaffolding", Ok: false,
			Details: ".golemic/config.json missing"}
	}

	// Validate project name before creating anything
	projectName := filepath.Base(p.repoRoot)
	if projectName == "" || projectName == "." {
		return Result{Name: ".golemic/ Scaffolding", Ok: false,
			Details: "cannot determine project name from repo root"}
	}
	if err := credentials.ValidateProjectName(projectName); err != nil {
		return Result{Name: ".golemic/ Scaffolding", Ok: false,
			Details: "invalid project name: " + err.Error()}
	}

	// config.json via encoding/json (JSON-safe, no template injection)
	if err := p.createConfig(golemicDir, configPath, projectName); err != nil {
		return Result{Name: ".golemic/ Scaffolding", Ok: false, Details: "failed to create config.json: " + err.Error()}
	}

	// guidelines directory
	guidelinesDir := filepath.Join(golemicDir, "guidelines")
	if err := os.MkdirAll(guidelinesDir, 0755); err != nil {
		return Result{Name: ".golemic/ Scaffolding", Ok: false, Details: "failed to create guidelines directory: " + err.Error()}
	}

	// dev.md
	if err := p.copyFromTemplate(guidelinesDir, "templates/guidelines/dev.md", "dev.md"); err != nil {
		return Result{Name: ".golemic/ Scaffolding", Ok: false, Details: "failed to create guidelines/dev.md: " + err.Error()}
	}

	// reviewer.md
	if err := p.copyFromTemplate(guidelinesDir, "templates/guidelines/reviewer.md", "reviewer.md"); err != nil {
		return Result{Name: ".golemic/ Scaffolding", Ok: false, Details: "failed to create guidelines/reviewer.md: " + err.Error()}
	}

	return Result{Name: ".golemic/ Scaffolding", Ok: false, Details: "created — please fill in config.json and guidelines"}
}

// createConfig marshals a config.Config with defaults and writes it to configPath
// using the shared writeFileAtomic helper. Idempotent: does not overwrite.
func (p *Preflight) createConfig(golemicDir, configPath, projectName string) error {
	cfg := config.DefaultConfig(projectName)
	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// writeFileAtomic creates parent dirs and enforces idempotency
	err = writeFileAtomic(configPath, data, 0644)
	if errors.Is(err, fs.ErrExist) {
		return nil // already exists, idempotent
	}
	return err
}

// credentialsSkeleton mirrors credentials.credentialsFile for scaffolding.
type credentialsSkeleton struct {
	DevToken      string `json:"dev_token"`
	ReviewerToken string `json:"reviewer_token"`
}

// createCredentialsSkeleton writes the credentials.json skeleton file.
// Idempotent: does not overwrite existing file.
// copyFromTemplate copies an embedded template file to the target directory.
// It does not overwrite an existing file.
func (p *Preflight) copyFromTemplate(targetDir, embeddedPath, targetName string) error {
	targetPath := filepath.Join(targetDir, targetName)

	// Check if file already exists (idempotency)
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	}

	content, err := templateFS.ReadFile(embeddedPath)
	if err != nil {
		return fmt.Errorf("read template %s: %w", embeddedPath, err)
	}

	if err := os.WriteFile(targetPath, content, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// checkConfig validates the .golemic/config.json using the config loader.
// The result is cached for reuse by checkCredentials.
func (p *Preflight) checkConfig() Result {
	cfg, err := config.Load(p.repoRoot)
	p.cachedCfg = cfg
	p.configDone = true
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return Result{Name: "config.json valide", Ok: false, Details: ".golemic/config.json missing"}
		}
		return Result{Name: "config.json valide", Ok: false, Details: err.Error()}
	}
	return Result{Name: "config.json valide", Ok: true}
}

// checkCredentials validates GitHub tokens.
// Setup mode: scaffolds credentials.json if missing, then validates via gh api user.
// Check mode: loads credentials without scaffolding, compares token values locally.
func (p *Preflight) checkCredentials() Result { //nolint:cyclop // linear sequence of independent guard clauses; extracting sub-functions would obscure the validation flow
	// Use cached config from checkConfig if available, otherwise load fresh
	var cfg *config.Config
	var err error
	if p.configDone && p.cachedCfg != nil {
		cfg = p.cachedCfg
	} else {
		cfg, err = config.Load(p.repoRoot)
	}
	if err != nil {
		return Result{Name: "Credentials", Ok: false, Details: "cannot load config: " + err.Error()}
	}

	if p.checkMode {
		// Check mode: no scaffolding, local token-value comparison only (no gh api call)
		loader := credentials.NewLoader(p.homeDir)
		loader.LookupEnv = p.lookupEnv
		creds, loadErr := loader.Load(cfg.Project)
		if loadErr != nil {
			return Result{Name: "Credentials", Ok: false, Details: loadErr.Error()}
		}
		if creds.DevToken() == creds.ReviewerToken() {
			return Result{Name: "Credentials", Ok: false, Details: "tokens identical"}
		}
		return Result{Name: "Credentials", Ok: true}
	}

	// Setup mode: scaffold credentials.json if missing (transparent side effect, §2.9).
	credPath := filepath.Join(p.homeDir, ".golemic", cfg.Project, "credentials.json")
	if _, statErr := os.Stat(credPath); os.IsNotExist(statErr) {
		if valErr := credentials.ValidateProjectName(cfg.Project); valErr == nil {
			skeleton := credentialsSkeleton{
				DevToken:      "${GOLEMIC_DEV_TOKEN}",
				ReviewerToken: "${GOLEMIC_REVIEWER_TOKEN}",
			}
			data, marshalErr := json.MarshalIndent(skeleton, "", "    ")
			if marshalErr == nil {
				_ = writeFileAtomic(credPath, data, 0600)
			}
		}
	}

	// Load credentials
	loader := credentials.NewLoader(p.homeDir)
	loader.LookupEnv = p.lookupEnv
	creds, err := loader.Load(cfg.Project)
	if err != nil {
		return Result{Name: "Credentials", Ok: false, Details: err.Error()}
	}

	// Validate dev token
	devLogin, err := p.ghWhoami(creds.DevToken())
	if err != nil {
		return Result{Name: "Credentials", Ok: false, Details: "dev token invalid: " + sanitizeErr(err)}
	}

	// Validate reviewer token
	revLogin, err := p.ghWhoami(creds.ReviewerToken())
	if err != nil {
		return Result{Name: "Credentials", Ok: false, Details: "reviewer token invalid: " + sanitizeErr(err)}
	}

	// Check that logins are different (§2.8)
	if devLogin == revLogin {
		return Result{Name: "Credentials", Ok: false,
			Details: fmt.Sprintf("dev and reviewer token use the same account (%s); they must be different", devLogin)}
	}

	return Result{Name: "Credentials", Ok: true,
		Details: fmt.Sprintf("dev=%s, reviewer=%s", creds.DevSource(), creds.ReviewerSource())}
}

// ghWhoami runs `gh api user` with the given GH_TOKEN and returns the login name.
func (p *Preflight) ghWhoami(token string) (string, error) {
	out, err := p.executor.RunWithEnv(
		map[string]string{"GH_TOKEN": token},
		"gh", "api", "user",
	)
	if err != nil {
		var ee *ErrExit
		if errors.As(err, &ee) {
			return "", fmt.Errorf("gh api user: exit code %d", ee.ExitCode)
		}
		return "", fmt.Errorf("gh api user: %s", err.Error())
	}

	var user ghUserLogin
	if err := json.Unmarshal([]byte(out), &user); err != nil {
		return "", fmt.Errorf("gh api user: invalid response")
	}
	if user.Login == "" {
		return "", fmt.Errorf("gh api user: empty login")
	}

	return user.Login, nil
}

// ghLabelEntry holds the name field from `gh label list --json name`.
type ghLabelEntry struct {
	Name string `json:"name"`
}

// requiredLabels lists the labels that must exist in the repository.
var requiredLabels = []struct {
	name        string
	color       string
	description string
}{
	{"in-progress", "fbca04", "Issue is currently claimed by an autonomous runner"},
	{"needs-human", "d93f0b", "Autonomous runner failed; requires human triage"},
}

// checkLabels verifies that the required GitHub labels exist.
// Setup mode: creates missing labels via gh label create.
// Check mode: reports missing labels as failures without any mutation.
func (p *Preflight) checkLabels() Result { //nolint:cyclop // linear sequence of independent guard clauses; extracting sub-functions would obscure the validation flow
	var cfg *config.Config
	var err error
	if p.configDone && p.cachedCfg != nil {
		cfg = p.cachedCfg
	} else {
		cfg, err = config.Load(p.repoRoot)
	}
	if err != nil {
		return Result{Name: "labels", Ok: false, Details: "cannot load config: " + err.Error()}
	}

	loader := credentials.NewLoader(p.homeDir)
	loader.LookupEnv = p.lookupEnv
	creds, err := loader.Load(cfg.Project)
	if err != nil {
		return Result{Name: "labels", Ok: false, Details: "cannot load credentials: " + err.Error()}
	}

	env := map[string]string{"GH_TOKEN": creds.DevToken()}

	out, err := p.executor.RunWithEnv(env, "gh", "label", "list", "--json", "name")
	if err != nil {
		return Result{Name: "labels", Ok: false, Details: "gh label list failed: " + sanitizeErr(err)}
	}

	var entries []ghLabelEntry
	if jsonErr := json.Unmarshal([]byte(out), &entries); jsonErr != nil {
		return Result{Name: "labels", Ok: false, Details: "gh label list: invalid response"}
	}

	existing := make(map[string]bool, len(entries))
	for _, e := range entries {
		existing[e.Name] = true
	}

	var missing []string
	for _, req := range requiredLabels {
		if !existing[req.name] {
			missing = append(missing, req.name)
		}
	}

	if p.checkMode {
		if len(missing) > 0 {
			return Result{Name: "labels", Ok: false, Details: "missing labels: " + strings.Join(missing, ", ")}
		}
		return Result{Name: "labels", Ok: true}
	}

	for _, req := range requiredLabels {
		if existing[req.name] {
			continue
		}
		if _, createErr := p.executor.RunWithEnv(env, "gh", "label", "create", req.name,
			"--color", req.color, "--description", req.description); createErr != nil {
			return Result{Name: "labels", Ok: false,
				Details: "gh label create " + req.name + " failed: " + sanitizeErr(createErr)}
		}
	}

	return Result{Name: "labels", Ok: true}
}

// sanitizeErr returns a safe, prefix-only error message — never raw stderr payload.
func sanitizeErr(err error) string {
	var ee *ErrExit
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit code %d", ee.ExitCode)
	}
	return err.Error()
}