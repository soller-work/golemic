package preflight

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicCreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")
	content := []byte(`{"key": "value"}`)

	if err := writeFileAtomic(path, content, 0644); err != nil {
		t.Fatalf("writeFileAtomic() unexpected error: %v", err)
	}

	// Verify file exists with correct content
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("file content = %q, want %q", string(got), string(content))
	}
}

func TestWriteFileAtomicDoesNotOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")
	original := []byte(`"original content"`)

	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	// Second write should fail with fs.ErrExist
	err := writeFileAtomic(path, []byte(`"new content"`), 0644)
	if err == nil {
		t.Fatal("writeFileAtomic() should fail when file exists")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("writeFileAtomic() error should wrap fs.ErrExist, got: %v", err)
	}

	// Verify original content was not overwritten
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("file was overwritten: got %q, want %q", string(got), string(original))
	}
}

func TestWriteFileAtomicPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")

	if err := writeFileAtomic(path, []byte(`{}`), 0600); err != nil {
		t.Fatalf("writeFileAtomic() unexpected error: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	perm := fi.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = 0%o, want 0600", perm)
	}
}

func TestWriteFileAtomicCreatesMissingParent(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "a", "b", "c")
	path := filepath.Join(nestedDir, "test.json")

	// Verify parent does NOT exist before write
	if _, err := os.Stat(nestedDir); err == nil {
		t.Fatal("nested dir should not exist before write")
	}

	if err := writeFileAtomic(path, []byte(`{}`), 0600); err != nil {
		t.Fatalf("writeFileAtomic() unexpected error: %v", err)
	}

	// Verify parent directories were created with 0755
	dirInfo, err := os.Stat(nestedDir)
	if err != nil {
		t.Fatalf("parent directory should exist after write: %v", err)
	}
	if !dirInfo.IsDir() {
		t.Errorf("parent path should be a directory")
	}
	// 0755 on macOS often reports as drwxr-xr-x; check the permission bits
	if dirInfo.Mode().Perm()&0755 != 0755 {
		t.Errorf("parent directory permissions too restrictive: 0%o", dirInfo.Mode().Perm())
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("created file should exist: %v", err)
	}
}

func TestWriteFileAtomicReused(t *testing.T) {
	// AC-007: createConfig uses the shared writeFileAtomic helper.
	// Credentials scaffolding is now inline in checkCredentials (transparent side effect),
	// no longer a separate function to test here.

	exec := fakeExecutorOK()
	p, _, repoRoot := setupPreflight(t, exec)

	// ===== config.json via createConfig (uses writeFileAtomic with 0644) =====
	golemicDir := filepath.Join(repoRoot, ".golemic")
	configPath := filepath.Join(golemicDir, "config.json")
	projectName := filepath.Base(repoRoot)

	if err := p.createConfig(golemicDir, configPath, projectName); err != nil {
		t.Fatalf("createConfig() unexpected error: %v", err)
	}

	// Verify config.json permissions are 0644
	cfgFi, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgFi.Mode().Perm() != 0644 {
		t.Errorf("config.json perms = 0%o, want 0644", cfgFi.Mode().Perm())
	}

	// Verify idempotency: second call should not overwrite
	if err := p.createConfig(golemicDir, configPath, projectName); err != nil {
		t.Errorf("createConfig() second call (idempotent) should succeed, got: %v", err)
	}
}
