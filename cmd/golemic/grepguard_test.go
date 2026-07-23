package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golemic/internal/prompt"
)

// TestGrepGuard_NoGhPrReview enforces BR-008: the literal "gh pr review" invocation
// pattern must not appear in any production Go source file. Any violation risks a
// silent resurrection of the legacy submission path.
//
// Test files (*_test.go) are excluded because they may legitimately mention the
// pattern in assertions that verify the pattern is absent.
func TestGrepGuard_NoGhPrReview(t *testing.T) { //nolint:cyclop
	root := findModuleRoot(t)

	// Pattern that would appear in actual gh-invocation code (not in assertions).
	// This matches both "pr", "review" as separate string args and the inline form.
	const badPattern = `"pr", "review"`

	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only scan production Go files; test files may reference the pattern in assertions.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), badPattern) {
			rel, _ := filepath.Rel(root, path)
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk error: %v", err)
	}
	if len(violations) > 0 {
		t.Errorf("BR-008: 'gh pr review' invocation pattern found in production Go files (must use GraphQL exclusively):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// detectExecutableGhLines returns lines that appear to be executable raw gh command
// instructions. Lines that express a prohibition (e.g. "Do not run `gh pr create`")
// are excluded.
func detectExecutableGhLines(text string) []string {
	var violations []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "`gh ") && !isGhProhibitionLine(line) {
			violations = append(violations, strings.TrimSpace(line))
		}
	}
	return violations
}

// isGhProhibitionLine returns true when the line prohibits rather than instructs gh usage.
func isGhProhibitionLine(line string) bool {
	lower := strings.ToLower(line)
	for _, phrase := range []string{"do not run", "do not use", "not to run", "not to use", "must not run", "must not use", "cannot run"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// TestGrepGuard_AgentFacing_AllowsProhibitions verifies that prohibition text such as
// "Do not run `gh pr create`" is not flagged as an executable instruction.
func TestGrepGuard_AgentFacing_AllowsProhibitions(t *testing.T) {
	allowed := []string{
		"> **Important:** Do not run `gh pr create` — use golemic open-pr instead.",
		"Do not use `gh pr view` — use golemic pr-view instead.",
		"Must not run `gh api` calls directly.",
	}
	for _, line := range allowed {
		if got := detectExecutableGhLines(line); len(got) > 0 {
			t.Errorf("prohibition line incorrectly flagged: %q", line)
		}
	}
}

// TestGrepGuard_AgentFacing_DetectsViolation verifies that a positive instruction
// to run a raw gh command is detected as a violation.
func TestGrepGuard_AgentFacing_DetectsViolation(t *testing.T) {
	violations := []string{
		"2. Fetch the diff: run `gh pr view 123` to inspect the PR.",
		"Use `gh issue list` to see open issues.",
		"Run `gh api repos/owner/repo` to fetch metadata.",
	}
	for _, line := range violations {
		if got := detectExecutableGhLines(line); len(got) == 0 {
			t.Errorf("executable gh instruction not detected: %q", line)
		}
	}
}

// TestGrepGuard_AgentFacing_CurrentSourcesClean scans all agent-facing prompt
// templates and .golemic persona/guideline files for executable raw gh instructions.
func TestGrepGuard_AgentFacing_CurrentSourcesClean(t *testing.T) { //nolint:cyclop
	root := findModuleRoot(t)
	sources := make(map[string]string)
	for k, v := range renderedPromptSources(t) {
		sources[k] = v
	}
	for k, v := range golmicAgentFacingFiles(t, root) {
		sources[k] = v
	}
	var violations []string
	for name, text := range sources {
		for _, line := range detectExecutableGhLines(text) {
			violations = append(violations, fmt.Sprintf("%s: %s", name, line))
		}
	}
	if len(violations) > 0 {
		t.Errorf("agent-facing sources contain executable raw gh instructions:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// renderedPromptSources renders all prompt templates with placeholder data and
// returns a label→rendered-text map.
func renderedPromptSources(t *testing.T) map[string]string { //nolint:cyclop
	t.Helper()
	tmpDir := t.TempDir()
	guidelinesPath := filepath.Join(tmpDir, "guidelines.md")
	if err := os.WriteFile(guidelinesPath, []byte("# Guidelines"), 0600); err != nil {
		t.Fatalf("write guidelines: %v", err)
	}
	issue := prompt.Issue{Number: 1, Title: "test"}
	branch, verifyCmd := "branch", "go test ./..."
	out := make(map[string]string)

	dev, err := prompt.RenderDev(issue, branch, verifyCmd, guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDev: %v", err)
	}
	out["prompt:dev"] = dev

	reviewer, err := prompt.RenderReviewer(1, issue, verifyCmd, guidelinesPath, false, "")
	if err != nil {
		t.Fatalf("RenderReviewer: %v", err)
	}
	out["prompt:reviewer"] = reviewer

	devRetry, err := prompt.RenderDevRetry("findings", "", issue, branch, verifyCmd, guidelinesPath, false)
	if err != nil {
		t.Fatalf("RenderDevRetry: %v", err)
	}
	out["prompt:devRetry"] = devRetry

	devCIRetry, err := prompt.RenderDevCIRetry("check failed", issue, branch, verifyCmd, guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDevCIRetry: %v", err)
	}
	out["prompt:devCIRetry"] = devCIRetry

	devRebase, err := prompt.RenderDevRebaseConflictResolve(1, branch, "main", []string{"foo.go"}, verifyCmd, guidelinesPath)
	if err != nil {
		t.Fatalf("RenderDevRebaseConflictResolve: %v", err)
	}
	out["prompt:devRebase"] = devRebase

	return out
}

// golmicAgentFacingFiles reads .golemic/agents and .golemic/guidelines markdown files
// and returns a relative-path→content map.
func golmicAgentFacingFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for _, dir := range []string{
		filepath.Join(root, ".golemic", "agents"),
		filepath.Join(root, ".golemic", "guidelines"),
	} {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("readdir %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			rel, _ := filepath.Rel(root, path)
			out[rel] = string(data)
		}
	}
	return out
}

// findModuleRoot walks up from the test binary's working directory to find go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

// TestGrepGuard_NoMCPWiring enforces BR-C6: the dead MCP integration surfaces
// (.mcp.json, WriteMCPFiles, mcpServers, --approve for pi) must not reappear
// in any production Go source file under internal/ or cmd/.
func TestGrepGuard_NoMCPWiring(t *testing.T) { //nolint:cyclop
	root := findModuleRoot(t)
	badPatterns := []string{".mcp.json", "WriteMCPFiles", "mcpServers", `"--approve"`}

	var violations []string
	for _, searchRoot := range []string{
		filepath.Join(root, "internal"),
		filepath.Join(root, "cmd"),
	} {
		walkErr := filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name == ".git" || name == "vendor" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			rel, _ := filepath.Rel(root, path)
			for _, pat := range badPatterns {
				if strings.Contains(string(data), pat) {
					violations = append(violations, rel+": "+pat)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk error under %s: %v", searchRoot, walkErr)
		}
	}
	if len(violations) > 0 {
		t.Errorf("BR-C6: dead MCP wiring found in production Go files:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
