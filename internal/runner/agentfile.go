package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golemic/internal/agentfile"
)

// resolveAgentFile resolves the persona for the given role.
// If <repoRoot>/.golemic/agents/{role}.md exists, it is used as an override.
// Otherwise the canonical embedded persona is used. A missing repo file is not an error.
func (r *Runner) resolveAgentFile(role string) (systemPromptFile, model string, cleanup func(), err error) {
	path := filepath.Join(r.repoRoot, ".golemic", "agents", role+".md")

	var chain []string
	var body string

	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		chain, body, err = agentfile.Read(path)
		if err != nil {
			return "", "", func() {}, err
		}
	case os.IsNotExist(statErr):
		chain, body, err = agentfile.EmbeddedPersona(role)
		if err != nil {
			return "", "", func() {}, err
		}
	default:
		return "", "", func() {}, fmt.Errorf("resolveAgentFile: stat %s: %w", path, statErr)
	}

	tmp, err := os.CreateTemp("", "golemic-"+role+"-*.md")
	if err != nil {
		return "", "", func() {}, fmt.Errorf("resolveAgentFile: create temp for %s: %w", role, err)
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()           //nolint:errcheck
		os.Remove(tmp.Name()) //nolint:errcheck
		return "", "", func() {}, fmt.Errorf("resolveAgentFile: write temp for %s: %w", role, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name()) //nolint:errcheck
		return "", "", func() {}, fmt.Errorf("resolveAgentFile: close temp for %s: %w", role, err)
	}

	name := tmp.Name()
	return name, strings.Join(chain, ", "), func() { os.Remove(name) }, nil //nolint:errcheck
}
