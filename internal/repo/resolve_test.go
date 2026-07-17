package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fakeExecutor answers `git rev-parse --show-toplevel` per working directory.
// The key "" is the cwd call (no -C); other keys are the dir passed via -C.
type fakeExecutor struct {
	roots map[string]string
}

func (f fakeExecutor) Run(name string, args ...string) (string, error) {
	dir := ""
	for i, a := range args {
		if a == "-C" && i+1 < len(args) {
			dir = args[i+1]
		}
	}
	if root, ok := f.roots[dir]; ok {
		return root, nil
	}
	return "", fmt.Errorf("not a git repository: %s", dir)
}

func (f fakeExecutor) RunWithEnv(_ map[string]string, name string, args ...string) (string, error) {
	return f.Run(name, args...)
}

func (f fakeExecutor) RunInDir(_ string, name string, args ...string) (string, error) {
	return f.Run(name, args...)
}

func (f fakeExecutor) RunWithEnvInDir(_ map[string]string, _ string, name string, args ...string) (string, error) {
	return f.Run(name, args...)
}

func TestResolveHostRepo_SelfRepo(t *testing.T) {
	root := t.TempDir()
	exec := fakeExecutor{roots: map[string]string{"": root}}

	got, err := ResolveHostRepo(exec, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != root {
		t.Fatalf("got %q, want %q", got, root)
	}
}

func TestResolveHostRepo_SubdirOfSelf(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "internal", "runner")
	exec := fakeExecutor{roots: map[string]string{"": root}}

	got, err := ResolveHostRepo(exec, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != root {
		t.Fatalf("got %q, want %q", got, root)
	}
}

// Real subdirectory install: golemic files physically live inside the host repo,
// so git resolves the host root directly and cwd is inside it.
func TestResolveHostRepo_RealSubdirInstall(t *testing.T) {
	host := t.TempDir()
	cwd := filepath.Join(host, "vendor", "golemic")
	exec := fakeExecutor{roots: map[string]string{"": host}}

	got, err := ResolveHostRepo(exec, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != host {
		t.Fatalf("got %q, want %q", got, host)
	}
}

// Symlink install at an ARBITRARY location: cwd is outside the git-resolved root
// (git followed the symlink into golemic's own checkout). The host repo must be
// found by walking up the logical cwd path — with no hardcoded path component.
func TestResolveHostRepo_SymlinkInstall_ArbitraryLocation(t *testing.T) {
	host := t.TempDir()
	golemicCheckout := "/actual/golemic"

	// Arbitrary install dir under the host repo (not tools/golemic).
	cwd := filepath.Join(host, "vendor", "tooling", "golemic")
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(host, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	exec := fakeExecutor{roots: map[string]string{
		"":   golemicCheckout, // cwd call follows symlink → golemic's own root
		cwd:  golemicCheckout, // -C into the symlinked dir → still golemic
		host: host,            // -C into the host root → host
	}}

	got, err := ResolveHostRepo(exec, cwd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != host {
		t.Fatalf("got %q, want %q (expected host repo via upward walk)", got, host)
	}
}
