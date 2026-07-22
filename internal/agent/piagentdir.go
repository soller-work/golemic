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

// preparePiAgentDir ensures ~/.golemic/pi is seeded from localPiAgentDir and
// returns the path to the golemic-owned agent dir. Idempotent and safe under
// concurrent calls. Fails closed if localPiAgentDir does not exist (BR-7).
func preparePiAgentDir(localPiAgentDir string) (string, error) {
	if _, err := os.Stat(localPiAgentDir); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("pi agent dir not found at %s; pi must be installed (auto-install not yet supported)", localPiAgentDir)
		}
		return "", fmt.Errorf("agent: stat local pi agent dir %q: %w", localPiAgentDir, err)
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

	if err := deriveSettings(localPiAgentDir, golemicPiDir); err != nil {
		return "", err
	}

	return golemicPiDir, nil
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
