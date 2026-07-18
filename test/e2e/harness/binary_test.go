package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindBinary(t *testing.T) {
	writeRegular := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("directory named golemic is rejected", func(t *testing.T) {
		// Reproduces the GitHub Actions checkout layout
		// /home/runner/work/golemic/golemic, where the parent dir contains a
		// directory named "golemic" that must not be mistaken for the binary.
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "golemic"), 0o755); err != nil {
			t.Fatal(err)
		}
		start := filepath.Join(root, "golemic", "sub")
		if err := os.Mkdir(start, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := FindBinary(start, ""); got != "" {
			t.Errorf("expected no binary (directory must be rejected), got %q", got)
		}
	})

	t.Run("regular file named golemic is accepted via tree walk", func(t *testing.T) {
		root := t.TempDir()
		bin := filepath.Join(root, "golemic")
		writeRegular(t, bin)
		start := filepath.Join(root, "sub")
		if err := os.Mkdir(start, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := FindBinary(start, ""); got != bin {
			t.Errorf("expected %q, got %q", bin, got)
		}
	})

	t.Run("symlink to regular file is accepted", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real-golemic")
		writeRegular(t, target)
		link := filepath.Join(root, "golemic")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if got := FindBinary(root, ""); got != link {
			t.Errorf("expected symlink %q to be accepted, got %q", link, got)
		}
	})

	t.Run("GOLEMIC_BINARY pointing at a directory is rejected", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "not-a-binary")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := FindBinary(root, dir); got != "" {
			t.Errorf("expected env override to a directory to be rejected, got %q", got)
		}
	})

	t.Run("GOLEMIC_BINARY pointing at a regular file is used directly", func(t *testing.T) {
		root := t.TempDir()
		bin := filepath.Join(root, "custom-golemic")
		writeRegular(t, bin)
		if got := FindBinary(t.TempDir(), bin); got != bin {
			t.Errorf("expected env override %q, got %q", bin, got)
		}
	})

	t.Run("no binary anywhere returns empty", func(t *testing.T) {
		if got := FindBinary(t.TempDir(), ""); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}
