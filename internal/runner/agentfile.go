package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golemic/internal/agentfile"
)

// resolveAgentFile reads .golemic/agents/{role}.md, parses its frontmatter, and
// writes the persona body to a temporary file. Returns the temp file path,
// comma-separated model chain, and a cleanup function that removes the temp file.
//
// Errors hard on missing file, missing model: key, or empty chain — no silent defaults.
func (r *Runner) resolveAgentFile(role string) (systemPromptFile, model string, cleanup func(), err error) {
	path := filepath.Join(r.repoRoot, ".golemic", "agents", role+".md")
	chain, body, err := agentfile.Read(path)
	if err != nil {
		return "", "", func() {}, err
	}

	tmp, err := os.CreateTemp("", "golemic-"+role+"-*.md")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("resolveAgentFile: create temp for %s: %w", role, err)
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()                 //nolint:errcheck
		os.Remove(tmp.Name())       //nolint:errcheck
		return "", "", func() {}, fmt.Errorf("resolveAgentFile: write temp for %s: %w", role, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())       //nolint:errcheck
		return "", "", func() {}, fmt.Errorf("resolveAgentFile: close temp for %s: %w", role, err)
	}

	name := tmp.Name()
	return name, strings.Join(chain, ", "), func() { os.Remove(name) }, nil //nolint:errcheck
}
