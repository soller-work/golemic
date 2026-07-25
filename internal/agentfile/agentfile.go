// Package agentfile reads .golemic/agents/{role}.md files, parses their YAML
// frontmatter, and returns the model chain and I/O-neutral persona body.
package agentfile

import (
	"embed"
	"fmt"
	"os"
	"strings"

	"golemic/internal/agent"
)

//go:embed personas/dev.md personas/reviewer.md
var personaFS embed.FS

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
	return Parse(data, path)
}

// Parse parses the frontmatter and body from data, using name for error messages.
// Returns the model chain and frontmatter-stripped body.
func Parse(data []byte, name string) (modelChain []string, body string, err error) {
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return nil, "", fmt.Errorf("agentfile: %s: no frontmatter block (file must start with ---)", name)
	}

	rest := content[4:] // skip opening "---\n"
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return nil, "", fmt.Errorf("agentfile: %s: unterminated frontmatter block", name)
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
		return nil, "", fmt.Errorf("agentfile: %s: missing or empty model: key in frontmatter", name)
	}

	chain, err := agent.ParseModelChain(modelRaw)
	if err != nil {
		return nil, "", fmt.Errorf("agentfile: %s: %w", name, err)
	}

	return chain, body, nil
}

// EmbeddedPersona returns the model chain and body of the canonical embedded
// persona for the given role ("dev" or "reviewer"). It is the default when no
// per-repo override file exists.
func EmbeddedPersona(role string) (modelChain []string, body string, err error) {
	path := "personas/" + role + ".md"
	data, err := personaFS.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("agentfile: embedded persona for %s: %w", role, err)
	}
	return Parse(data, path)
}
