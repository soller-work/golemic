package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupLabelsTest creates the standard config and credentials files needed for checkLabels.
func setupLabelsTest(t *testing.T, p *Preflight, homeDir, repoRoot string) {
	t.Helper()
	projectName := filepath.Base(repoRoot)

	golemicDir := filepath.Join(repoRoot, ".golemic")
	if err := os.MkdirAll(golemicDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(golemicDir, "config.json"), []byte(`{"project":"`+projectName+`","verify_command":"go test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	credDir := filepath.Join(homeDir, ".golemic", projectName)
	if err := os.MkdirAll(credDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), []byte(`{"dev_token":"ghp_dev_token","reviewer_token":"ghp_rev_token"}`), 0600); err != nil {
		t.Fatal(err)
	}

	p.SetLookupEnv(func(string) (string, bool) { return "", false })
}

// AC-001: setup mode on fresh repo — both labels missing → gh label list + two gh label create calls.
func TestCheckLabels_SetupMode_BothMissing(t *testing.T) { //nolint:cyclop,gocognit // exhaustive AC-001 sequence assertions; linear flow, extracting helpers would obscure scenario coverage
	var calls []string
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			token := env["GH_TOKEN"]
			if token == "" {
				t.Error("GH_TOKEN must be set for gh calls")
			}
			call := name + " " + strings.Join(args, " ")
			calls = append(calls, call)
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return `[]`, nil // no labels present
			}
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "create" {
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)

	result := p.checkLabels()

	if !result.Ok {
		t.Errorf("checkLabels() should return Ok=true, got Details: %s", result.Details)
	}
	if result.Name != "labels" {
		t.Errorf("result.Name = %q, want %q", result.Name, "labels")
	}

	// Verify call sequence: list first, then create for each missing label
	if len(calls) < 3 {
		t.Fatalf("expected at least 3 calls (list + 2 creates), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "label list") {
		t.Errorf("first call should be gh label list, got: %s", calls[0])
	}
	createCalls := calls[1:]
	foundInProgress := false
	foundNeedsHuman := false
	for _, c := range createCalls {
		if strings.Contains(c, "in-progress") {
			foundInProgress = true
		}
		if strings.Contains(c, "needs-human") {
			foundNeedsHuman = true
		}
	}
	if !foundInProgress {
		t.Error("expected gh label create in-progress call")
	}
	if !foundNeedsHuman {
		t.Error("expected gh label create needs-human call")
	}

	// Verify the dev token is never in result.Details
	if strings.Contains(result.Details, "ghp_") {
		t.Errorf("result.Details must not contain token values, got: %q", result.Details)
	}
}

// AC-001 extension: verify GH_TOKEN is the dev token and never appears in output.
func TestCheckLabels_GHTokenIsDevToken_NoLeak(t *testing.T) {
	var capturedToken string
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			capturedToken = env["GH_TOKEN"]
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return `[{"name":"in-progress"},{"name":"needs-human"}]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)

	result := p.checkLabels()

	if !result.Ok {
		t.Errorf("checkLabels() should return Ok=true, got Details: %s", result.Details)
	}
	if capturedToken != "ghp_dev_token" {
		t.Errorf("GH_TOKEN should be dev token, got: %q", capturedToken)
	}
	if strings.Contains(result.Details, capturedToken) {
		t.Errorf("result.Details must not contain token value, got: %q", result.Details)
	}
}

// AC-002: setup mode on already-scaffolded repo — only gh label list, no creates.
func TestCheckLabels_SetupMode_BothPresent(t *testing.T) { //nolint:cyclop
	var createCalled bool
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return `[{"name":"in-progress"},{"name":"needs-human"}]`, nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "create" {
				createCalled = true
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)

	result := p.checkLabels()

	if !result.Ok {
		t.Errorf("checkLabels() should return Ok=true when both labels exist, got Details: %s", result.Details)
	}
	if createCalled {
		t.Error("gh label create must not be called when both labels already exist")
	}
}

// AC-003: check mode with one missing label — FAILED, no create calls.
func TestCheckLabels_CheckMode_MissingLabel(t *testing.T) { //nolint:cyclop
	var createCalled bool
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return `[{"name":"in-progress"}]`, nil // needs-human missing
			}
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "create" {
				createCalled = true
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)
	p.checkMode = true

	result := p.checkLabels()

	if result.Ok {
		t.Error("checkLabels() should return Ok=false when a label is missing in check mode")
	}
	if !strings.Contains(result.Details, "needs-human") {
		t.Errorf("result.Details should mention missing label, got: %q", result.Details)
	}
	if !strings.Contains(result.Details, "missing labels") {
		t.Errorf("result.Details should contain 'missing labels', got: %q", result.Details)
	}
	if createCalled {
		t.Error("gh label create must not be called in check mode")
	}
}

// AC-003 extension: check mode with both labels missing reports both names.
func TestCheckLabels_CheckMode_BothMissing(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return `[]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)
	p.checkMode = true

	result := p.checkLabels()

	if result.Ok {
		t.Error("checkLabels() should return Ok=false when both labels are missing")
	}
	if !strings.Contains(result.Details, "in-progress") {
		t.Errorf("result.Details should mention in-progress, got: %q", result.Details)
	}
	if !strings.Contains(result.Details, "needs-human") {
		t.Errorf("result.Details should mention needs-human, got: %q", result.Details)
	}
}

// AC-004: gh label list failure — FAILED with underlying error, no create calls.
func TestCheckLabels_GhListFailure(t *testing.T) { //nolint:cyclop
	var createCalled bool
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return "", &ErrExit{ExitCode: 1, Stderr: "HTTP 401: token ghp_secret is invalid"}
			}
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "create" {
				createCalled = true
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)

	result := p.checkLabels()

	if result.Ok {
		t.Error("checkLabels() should return Ok=false when gh label list fails")
	}
	if !strings.Contains(result.Details, "gh label list failed") {
		t.Errorf("result.Details should mention 'gh label list failed', got: %q", result.Details)
	}
	if createCalled {
		t.Error("gh label create must not be called when gh label list fails")
	}
	// Verify no raw stderr in details (sanitizeErr should strip it)
	if strings.Contains(result.Details, "HTTP 401") {
		t.Errorf("result.Details must not contain raw stderr, got: %q", result.Details)
	}
	if strings.Contains(result.Details, "ghp_") {
		t.Errorf("result.Details must not contain token values, got: %q", result.Details)
	}
}

// Check mode: gh label list returns both labels → OK.
func TestCheckLabels_CheckMode_BothPresent(t *testing.T) {
	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return `[{"name":"in-progress"},{"name":"needs-human"}]`, nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)
	p.checkMode = true

	result := p.checkLabels()

	if !result.Ok {
		t.Errorf("checkLabels() should return Ok=true when both labels exist in check mode, got: %s", result.Details)
	}
}

// Verify label colors and descriptions are passed correctly to gh label create.
func TestCheckLabels_CreateCallsUseDT001Metadata(t *testing.T) { //nolint:cyclop,gocognit // comprehensive DT-001 metadata assertions; splitting helpers would obscure what is being verified
	type createArgs struct {
		name        string
		color       string
		description string
	}
	var creates []createArgs

	exec := &fakeExecutor{
		runWithEnvFunc: func(env map[string]string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "list" {
				return `[]`, nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "label" && args[1] == "create" {
				// args: create <name> --color <hex> --description <text>
				ca := createArgs{}
				for i, a := range args {
					switch a {
					case "create":
						if i+1 < len(args) {
							ca.name = args[i+1]
						}
					case "--color":
						if i+1 < len(args) {
							ca.color = args[i+1]
						}
					case "--description":
						if i+1 < len(args) {
							ca.description = args[i+1]
						}
					}
				}
				creates = append(creates, ca)
				return "", nil
			}
			return "", fmt.Errorf("not mocked: %s %v", name, args)
		},
	}

	p, homeDir, repoRoot := setupPreflight(t, exec)
	setupLabelsTest(t, p, homeDir, repoRoot)

	result := p.checkLabels()

	if !result.Ok {
		t.Fatalf("checkLabels() should return Ok=true, got: %s", result.Details)
	}
	if len(creates) != 2 {
		t.Fatalf("expected 2 label create calls, got %d", len(creates))
	}

	byName := make(map[string]createArgs)
	for _, c := range creates {
		byName[c.name] = c
	}

	ip, ok := byName["in-progress"]
	if !ok {
		t.Fatal("in-progress label not created")
	}
	if ip.color != "fbca04" {
		t.Errorf("in-progress color = %q, want fbca04", ip.color)
	}
	if !strings.Contains(ip.description, "autonomous runner") {
		t.Errorf("in-progress description = %q, expected to mention 'autonomous runner'", ip.description)
	}

	nh, ok := byName["needs-human"]
	if !ok {
		t.Fatal("needs-human label not created")
	}
	if nh.color != "d93f0b" {
		t.Errorf("needs-human color = %q, want d93f0b", nh.color)
	}
	if !strings.Contains(nh.description, "human triage") {
		t.Errorf("needs-human description = %q, expected to mention 'human triage'", nh.description)
	}
}
