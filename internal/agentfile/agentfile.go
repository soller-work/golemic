// Package agentfile reads .golemic/agents/{role}.md files, parses their YAML
// frontmatter, and returns the model chain and I/O-neutral persona body.
package agentfile

import (
	"fmt"
	"os"
	"strings"

	"golemic/internal/agent"
)

// Read reads the agent file at path, parses its YAML frontmatter, and returns
// the parsed model chain and the frontmatter-stripped body.
//
// Returns an error if:
//   - the file cannot be read (names the path)
//   - the file has no frontmatter block (must start with ---)
//   - the frontmatter has no model: key or the value is empty (names the file and key)
//   - the model chain is empty after parsing
func Read(path string) (modelChain []string, body string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("agentfile: %s: %w", path, err)
	}
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return nil, "", fmt.Errorf("agentfile: %s: no frontmatter block (file must start with ---)", path)
	}

	rest := content[4:] // skip opening "---\n"
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil, "", fmt.Errorf("agentfile: %s: unterminated frontmatter block", path)
	}

	frontmatter := rest[:end]
	body = strings.TrimPrefix(rest[end+5:], "\n") // skip "\n---\n" and optional leading newline

	modelRaw := ""
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.HasPrefix(line, "model:") {
			modelRaw = strings.TrimSpace(strings.TrimPrefix(line, "model:"))
			break
		}
	}

	if modelRaw == "" {
		return nil, "", fmt.Errorf("agentfile: %s: missing or empty model: key in frontmatter", path)
	}

	chain, err := agent.ParseModelChain(modelRaw)
	if err != nil {
		return nil, "", fmt.Errorf("agentfile: %s: %w", path, err)
	}

	return chain, body, nil
}
