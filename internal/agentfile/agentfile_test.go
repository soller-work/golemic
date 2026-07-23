package agentfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead_ParsesModelChainAndBody(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", `---
name: dev
model: A, B, C
---

Body content here.
`)

	chain, body, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(chain) != 3 || chain[0] != "A" || chain[1] != "B" || chain[2] != "C" {
		t.Errorf("chain = %v, want [A B C]", chain)
	}
	if body != "Body content here.\n" {
		t.Errorf("body = %q, want %q", body, "Body content here.\n")
	}
}

func TestRead_DeduplicatesModelChain(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", "---\nmodel: X, Y, X, Z\n---\nbody\n")

	chain, _, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(chain) != 3 || chain[0] != "X" || chain[1] != "Y" || chain[2] != "Z" {
		t.Errorf("chain = %v, want [X Y Z]", chain)
	}
}

func TestRead_MissingFile(t *testing.T) {
	_, _, err := Read("/nonexistent/path/agent.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got: %v", err)
	}
}

func TestRead_NoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", "# Just a body, no frontmatter\n")

	_, _, err := Read(path)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestRead_UnterminatedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", "---\nmodel: X\nno closing delimiter\n")

	_, _, err := Read(path)
	if err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestRead_MissingModelKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", "---\nname: dev\ndescription: a dev agent\n---\nbody\n")

	_, _, err := Read(path)
	if err == nil {
		t.Fatal("expected error for missing model key")
	}
	want := "missing or empty model:"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

func TestRead_EmptyModelKey(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", "---\nmodel:\n---\nbody\n")

	_, _, err := Read(path)
	if err == nil {
		t.Fatal("expected error for empty model key")
	}
}

func TestRead_ErrorNamesFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", "---\nname: dev\n---\nbody\n")

	_, _, err := Read(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file path %q", err.Error(), path)
	}
}

func TestRead_RealWorldFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "dev.md", `---
name: dev
description: Implements planned code changes.
tools: read,bash,write,edit
model: claude-bridge/claude-sonnet-4-6, claude-bridge/claude-haiku-4-5, openai-codex/gpt-5.4-mini, openrouter/deepseek/deepseek-v4-pro
---

You are a Dev agent.

## Mission

Implement the explicitly requested repository change.
`)

	chain, body, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(chain) != 4 {
		t.Errorf("chain length = %d, want 4; chain = %v", len(chain), chain)
	}
	if chain[0] != "claude-bridge/claude-sonnet-4-6" {
		t.Errorf("chain[0] = %q, want claude-bridge/claude-sonnet-4-6", chain[0])
	}
	if body == "" {
		t.Error("body must not be empty")
	}
}
