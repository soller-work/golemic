package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// resolveLocalPiAgentDir returns the local pi agent dir path.
// Uses PI_CODING_AGENT_DIR if set; otherwise defaults to ~/.pi/agent.
func resolveLocalPiAgentDir() (string, error) {
	if envDir := os.Getenv("PI_CODING_AGENT_DIR"); envDir != "" {
		return envDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agent: cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

// checkLocalPiDir returns an error if localPiAgentDir does not exist or cannot
// be stat'd, with a diagnostic message for the not-found case.
func checkLocalPiDir(localPiAgentDir string) error {
	_, err := os.Stat(localPiAgentDir)
	if err == nil {
		return nil
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("pi agent dir not found at %s; pi must be installed (auto-install not yet supported)", localPiAgentDir)
	}
	return fmt.Errorf("agent: stat local pi agent dir %q: %w", localPiAgentDir, err)
}

// preparePiAgentDir ensures ~/.golemic/pi is seeded from localPiAgentDir and
// returns the path to the golemic-owned agent dir. If gmExtensionSrcDir is
// non-empty and exists, the gm_ pi extension is provisioned at
// ~/.golemic/pi/extensions/golemic. Idempotent and safe under concurrent
// calls. Fails closed if localPiAgentDir does not exist (BR-7).
func preparePiAgentDir(localPiAgentDir, gmExtensionSrcDir string) (string, error) {
	if err := checkLocalPiDir(localPiAgentDir); err != nil {
		return "", err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agent: cannot determine home directory: %w", err)
	}

	golemicPiDir := filepath.Join(home, ".golemic", "pi")
	if err := os.MkdirAll(golemicPiDir, 0755); err != nil {
		return "", fmt.Errorf("agent: create golemic pi agent dir %q: %w", golemicPiDir, err)
	}

	entries, err := os.ReadDir(localPiAgentDir)
	if err != nil {
		return "", fmt.Errorf("agent: read local pi agent dir %q: %w", localPiAgentDir, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if name == "settings.json" {
			continue
		}
		target := filepath.Join(localPiAgentDir, name)
		dst := filepath.Join(golemicPiDir, name)
		if err := ensureSymlink(dst, target); err != nil {
			return "", err
		}
	}

	if err := seedGMExtension(golemicPiDir, gmExtensionSrcDir); err != nil {
		return "", err
	}

	if err := deriveSettings(localPiAgentDir, golemicPiDir); err != nil {
		return "", err
	}

	return golemicPiDir, nil
}

// seedGMExtension provisions the golemic gm_ pi extension into golemicPiDir/extensions/golemic.
// If gmExtensionSrcDir is empty or does not exist on disk, the call is a no-op.
// When golemicPiDir/extensions is currently a symlink, it is replaced with a real directory
// so the golemic extension can be added without modifying the user's pi agent dir.
func seedGMExtension(golemicPiDir, gmExtensionSrcDir string) error {
	if gmExtensionSrcDir == "" {
		return nil
	}
	if _, err := os.Stat(gmExtensionSrcDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("agent: stat gm extension source %q: %w", gmExtensionSrcDir, err)
	}
	extDir := filepath.Join(golemicPiDir, "extensions")
	if err := expandExtSymlink(extDir); err != nil {
		return err
	}
	if err := os.MkdirAll(extDir, 0755); err != nil {
		return fmt.Errorf("agent: create extensions dir %q: %w", extDir, err)
	}
	return ensureSymlink(filepath.Join(extDir, "golemic"), gmExtensionSrcDir)
}

// expandExtSymlink converts extDir from a symlink to a real directory,
// relinking each existing extension individually so golemic can add its own
// extension without writing into the user's pi agent dir.
func expandExtSymlink(extDir string) error {
	fi, err := os.Lstat(extDir)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return replaceSymlinkWithDir(extDir)
}

// replaceSymlinkWithDir removes a symlink at extDir, creates a real directory,
// and recreates individual symlinks for each entry in the original target.
func replaceSymlinkWithDir(extDir string) error {
	target, _ := os.Readlink(extDir)
	if err := os.Remove(extDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("agent: remove extensions symlink %q: %w", extDir, err)
	}
	if err := os.MkdirAll(extDir, 0755); err != nil {
		return fmt.Errorf("agent: create extensions dir %q: %w", extDir, err)
	}
	return relinkExtensions(extDir, target)
}

// relinkExtensions creates symlinks in extDir for each entry in srcDir,
// skipping the "golemic" entry (handled by seedGMExtension).
func relinkExtensions(extDir, srcDir string) error {
	if srcDir == "" {
		return nil
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return nil // srcDir unreadable; skip without error
	}
	for _, e := range entries {
		if e.Name() == "golemic" {
			continue
		}
		if err := ensureSymlink(filepath.Join(extDir, e.Name()), filepath.Join(srcDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ensureSymlink creates dst as a symlink pointing to target.
// A correct existing symlink is left as-is; a wrong or non-symlink entry is replaced.
// Safe under concurrent calls with identical (dst, target): last-writer-wins.
func ensureSymlink(dst, target string) error {
	existing, err := os.Readlink(dst)
	if err == nil && existing == target {
		return nil
	}
	if err == nil || !os.IsNotExist(err) {
		if rmErr := os.Remove(dst); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("agent: remove stale entry %q: %w", dst, rmErr)
		}
	}
	if symlinkErr := os.Symlink(target, dst); symlinkErr != nil {
		if !os.IsExist(symlinkErr) {
			return fmt.Errorf("agent: create symlink %q -> %q: %w", dst, target, symlinkErr)
		}
		return verifySymlinkConverged(dst, target, symlinkErr)
	}
	return nil
}

// verifySymlinkConverged checks that dst is a symlink pointing to target after a
// concurrent EEXIST: returns nil if another process already created the correct link.
func verifySymlinkConverged(dst, target string, origErr error) error {
	resolved, readErr := os.Readlink(dst)
	if readErr != nil || resolved != target {
		return fmt.Errorf("agent: create symlink %q -> %q: %w", dst, target, origErr)
	}
	return nil
}

// deriveSettings reads the local settings.json (empty object if absent),
// forces compaction.enabled=true, and writes the result to golemicPiDir/settings.json.
// All other settings keys are preserved (BR-2, BR-4).
func deriveSettings(localPiAgentDir, golemicPiDir string) error {
	settings := make(map[string]any)

	localSettingsPath := filepath.Join(localPiAgentDir, "settings.json")
	data, err := os.ReadFile(localSettingsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("agent: read local pi settings.json %q: %w", localSettingsPath, err)
	}
	if len(data) > 0 {
		if jsonErr := json.Unmarshal(data, &settings); jsonErr != nil {
			return fmt.Errorf("agent: parse local pi settings.json %q: %w", localSettingsPath, jsonErr)
		}
	}

	compaction, _ := settings["compaction"].(map[string]any)
	if compaction == nil {
		compaction = make(map[string]any)
	}
	compaction["enabled"] = true
	settings["compaction"] = compaction

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("agent: marshal golemic pi settings.json: %w", err)
	}

	dst := filepath.Join(golemicPiDir, "settings.json")
	return writeFileAtomic(golemicPiDir, dst, out)
}

// writeFileAtomic writes data to dst via a uniquely-named temp file in dir, then renames.
// Using a per-call temp name makes concurrent writers safe: last rename wins.
func writeFileAtomic(dir, dst string, data []byte) error {
	tmpFile, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp.*")
	if err != nil {
		return fmt.Errorf("agent: create temp file for %s: %w", dst, err)
	}
	tmp := tmpFile.Name()
	if _, err = tmpFile.Write(data); err != nil {
		tmpFile.Close() //nolint:errcheck
		os.Remove(tmp)  //nolint:errcheck
		return fmt.Errorf("agent: write %s: %w", dst, err)
	}
	if err = tmpFile.Close(); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("agent: close temp %s: %w", dst, err)
	}
	if err = os.Rename(tmp, dst); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return fmt.Errorf("agent: rename %s: %w", dst, err)
	}
	return nil
}
