package harness

import (
	"os"
	"path/filepath"
)

// isRegularFile reports whether path exists and is a regular file. os.Stat
// follows symlinks, so a symlink pointing at a regular binary still qualifies;
// a directory named "golemic" (as produced by the GitHub Actions checkout path
// /home/runner/work/golemic/golemic) does not.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// FindBinary locates the golemic binary. When envValue is a regular file it is
// used directly; otherwise the search walks up from startDir looking for a
// regular file named "golemic". Returns "" when no binary is found.
func FindBinary(startDir, envValue string) string {
	if isRegularFile(envValue) {
		return envValue
	}
	for dir := startDir; ; {
		if candidate := filepath.Join(dir, "golemic"); isRegularFile(candidate) {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
