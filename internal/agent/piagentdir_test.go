package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// makeLocalPiDir creates a fake local pi agent dir with the given settings.json content
// and a set of named entries (files or dirs). Returns the dir path.
func makeLocalPiDir(t *testing.T, settingsJSON string, entries ...string) string {
	t.Helper()
	dir := t.TempDir()
	if settingsJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settingsJSON), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range entries {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// golemicPiDir returns the expected golemic pi agent dir path for the given home.
func golemicPiDir(home string) string {
	return filepath.Join(home, ".golemic", "pi")
}

// TestPreparePiAgentDir_ForcesCompactionEnabled verifies that even when the local
// settings.json has compaction.enabled=false, the golemic-owned settings.json gets
// compaction.enabled=true (AC: compaction forced, local value overridden).
func TestPreparePiAgentDir_ForcesCompactionEnabled(t *testing.T) {
	localDir := makeLocalPiDir(t, `{"compaction":{"enabled":false},"defaultModel":"claude-3-5"}`)
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := preparePiAgentDir(localDir, "")
	if err != nil {
		t.Fatalf("preparePiAgentDir: %v", err)
	}

	settings := readSettings(t, filepath.Join(got, "settings.json"))
	assertCompactionEnabled(t, settings)
	if settings["defaultModel"] != "claude-3-5" {
		t.Errorf("defaultModel: got %v, want claude-3-5", settings["defaultModel"])
	}
}

// TestPreparePiAgentDir_CreatesSymlinks verifies that non-settings.json entries
// in the local pi agent dir become symlinks in the golemic-owned dir.
func TestPreparePiAgentDir_CreatesSymlinks(t *testing.T) {
	localDir := makeLocalPiDir(t, "", "auth.json", "models-store.json")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := preparePiAgentDir(localDir, "")
	if err != nil {
		t.Fatalf("preparePiAgentDir: %v", err)
	}

	assertSymlinks(t, got, localDir, "auth.json", "models-store.json")
}

// TestPreparePiAgentDir_PreservesOtherSettings verifies that packages, defaultProvider,
// and other keys from the local settings.json survive and only compaction.enabled changes.
func TestPreparePiAgentDir_PreservesOtherSettings(t *testing.T) {
	localSettings := `{
		"packages": ["npm:pi-claude-bridge"],
		"defaultProvider": "anthropic",
		"defaultModel": "claude-opus-4",
		"compaction": {"enabled": false, "reserveTokens": 16384}
	}`
	localDir := makeLocalPiDir(t, localSettings)
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := preparePiAgentDir(localDir, "")
	if err != nil {
		t.Fatalf("preparePiAgentDir: %v", err)
	}

	settings := readSettings(t, filepath.Join(got, "settings.json"))
	assertCompactionEnabled(t, settings)
	compaction, _ := settings["compaction"].(map[string]any)
	if compaction["reserveTokens"] == nil {
		t.Errorf("compaction.reserveTokens should be preserved, got nil")
	}
	if settings["defaultProvider"] != "anthropic" {
		t.Errorf("defaultProvider: got %v, want anthropic", settings["defaultProvider"])
	}
	if settings["defaultModel"] != "claude-opus-4" {
		t.Errorf("defaultModel: got %v, want claude-opus-4", settings["defaultModel"])
	}
	assertPackages(t, settings, "npm:pi-claude-bridge")
}

// TestPreparePiAgentDir_SettingsRederiveOnEachRun verifies that updating the local
// settings.json between runs causes the next preparePiAgentDir call to pick up the
// updated defaultModel while still forcing compaction.enabled=true.
func TestPreparePiAgentDir_SettingsRederiveOnEachRun(t *testing.T) {
	localDir := makeLocalPiDir(t, `{"defaultModel":"old-model"}`)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := preparePiAgentDir(localDir, ""); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Simulate user updating local settings between runs.
	if err := os.WriteFile(filepath.Join(localDir, "settings.json"), []byte(`{"defaultModel":"new-model"}`), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := preparePiAgentDir(localDir, "")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	settings := readSettings(t, filepath.Join(got, "settings.json"))
	if settings["defaultModel"] != "new-model" {
		t.Errorf("defaultModel: got %v, want new-model", settings["defaultModel"])
	}
	assertCompactionEnabled(t, settings)
}

// TestPreparePiAgentDir_Idempotent verifies that calling preparePiAgentDir twice
// produces the same result and does not error.
func TestPreparePiAgentDir_Idempotent(t *testing.T) {
	localDir := makeLocalPiDir(t, `{"compaction":{"enabled":false}}`, "auth.json")
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir1, err := preparePiAgentDir(localDir, "")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	dir2, err := preparePiAgentDir(localDir, "")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if dir1 != dir2 {
		t.Errorf("dir mismatch: %q vs %q", dir1, dir2)
	}

	assertSymlinks(t, dir2, localDir, "auth.json")
	assertCompactionEnabled(t, readSettings(t, filepath.Join(dir2, "settings.json")))
}

// TestPreparePiAgentDir_ConcurrentSafe verifies that concurrent preparations do not
// corrupt symlinks or settings.json.
func TestPreparePiAgentDir_ConcurrentSafe(t *testing.T) {
	localDir := makeLocalPiDir(t, `{"compaction":{"enabled":false}}`, "auth.json", "models-store.json")
	home := t.TempDir()
	t.Setenv("HOME", home)

	const goroutines = 8
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = preparePiAgentDir(localDir, "")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}

	gDir := golemicPiDir(home)
	assertSymlinks(t, gDir, localDir, "auth.json", "models-store.json")
	assertCompactionEnabled(t, readSettings(t, filepath.Join(gDir, "settings.json")))
}

// TestPreparePiAgentDir_NoWritesToLocalDir verifies that golemic never creates,
// modifies, or deletes any file under the local pi agent dir.
func TestPreparePiAgentDir_NoWritesToLocalDir(t *testing.T) {
	localDir := makeLocalPiDir(t, `{"compaction":{"enabled":false}}`, "auth.json", "models-store.json")
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Snapshot before: record all entries and their mtimes.
	before := snapshotDir(t, localDir)

	if _, err := preparePiAgentDir(localDir, ""); err != nil {
		t.Fatalf("preparePiAgentDir: %v", err)
	}

	after := snapshotDir(t, localDir)
	if len(before) != len(after) {
		t.Errorf("local dir entry count changed: %d -> %d", len(before), len(after))
	}
	for name, modBefore := range before {
		modAfter, ok := after[name]
		if !ok {
			t.Errorf("entry %q was deleted from local pi dir", name)
			continue
		}
		if modBefore != modAfter {
			t.Errorf("entry %q was modified in local pi dir (mtime changed)", name)
		}
	}
}

// TestPreparePiAgentDir_MissingLocalDir verifies that a non-existent local pi agent dir
// returns a diagnostic error and does not create the golemic dir.
func TestPreparePiAgentDir_MissingLocalDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	missing := filepath.Join(home, "does-not-exist", "pi", "agent")

	_, err := preparePiAgentDir(missing, "")
	if err == nil {
		t.Fatal("expected error for missing local pi agent dir, got nil")
	}
	if !contains(err.Error(), missing) {
		t.Errorf("error should name the missing path %q, got: %v", missing, err)
	}

	// Golemic pi dir must not have been created.
	if _, statErr := os.Stat(golemicPiDir(home)); statErr == nil {
		t.Errorf("golemic pi dir should not exist when local dir is missing")
	}
}

// TestPreparePiAgentDir_InvalidLocalSettingsJSON verifies that an unparseable local
// settings.json returns a diagnostic error.
func TestPreparePiAgentDir_InvalidLocalSettingsJSON(t *testing.T) {
	localDir := makeLocalPiDir(t, `not valid json`)
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := preparePiAgentDir(localDir, "")
	if err == nil {
		t.Fatal("expected error for invalid local settings.json, got nil")
	}
	if !contains(err.Error(), "settings.json") {
		t.Errorf("error should mention settings.json, got: %v", err)
	}
}

// TestPreparePiAgentDir_GMExtensionAndSiblingsSurvive verifies the full seedGMExtension
// path: when the local extensions dir is a symlink and a non-empty gmExtensionSrcDir is
// provided, preparePiAgentDir must (a) provision golemicPiDir/extensions/golemic pointing
// to gmExtensionSrcDir, and (b) preserve sibling extensions (e.g. "subagent") that were
// already in the local pi agent extensions dir.
func TestPreparePiAgentDir_GMExtensionAndSiblingsSurvive(t *testing.T) {
	// Build a local pi agent dir with extensions/subagent/ as a sibling extension.
	localDir := t.TempDir()
	subagentDir := filepath.Join(localDir, "extensions", "subagent")
	if err := os.MkdirAll(subagentDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Non-empty gm-extension source dir with a sentinel file.
	gmSrcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gmSrcDir, "index.ts"), []byte("export {}"), 0644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := preparePiAgentDir(localDir, gmSrcDir)
	if err != nil {
		t.Fatalf("preparePiAgentDir: %v", err)
	}

	// (a) golemicPiDir/extensions/golemic must be a symlink to gmSrcDir.
	golemicLink, err := os.Readlink(filepath.Join(got, "extensions", "golemic"))
	if err != nil {
		t.Fatalf("extensions/golemic: readlink: %v", err)
	}
	if golemicLink != gmSrcDir {
		t.Errorf("extensions/golemic → got %q, want %q", golemicLink, gmSrcDir)
	}

	// (b) The sibling "subagent" extension must survive in golemicPiDir/extensions/.
	if _, err := os.Stat(filepath.Join(got, "extensions", "subagent")); err != nil {
		t.Errorf("extensions/subagent: sibling must survive after symlink expansion: %v", err)
	}
}

// TestPreparePiAgentDir_AbsentLocalSettingsJSON verifies that when local settings.json
// is absent, the golemic-owned settings.json is still written with compaction.enabled=true.
func TestPreparePiAgentDir_AbsentLocalSettingsJSON(t *testing.T) {
	localDir := makeLocalPiDir(t, "" /* no settings.json */, "auth.json")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := preparePiAgentDir(localDir, "")
	if err != nil {
		t.Fatalf("preparePiAgentDir: %v", err)
	}
	assertCompactionEnabled(t, readSettings(t, filepath.Join(got, "settings.json")))
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// readSettings reads and unmarshals settings.json, fatally failing the test on error.
func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings.json %s: %v", path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings.json %s: %v", path, err)
	}
	return settings
}

// assertCompactionEnabled asserts that settings["compaction"]["enabled"] is true.
func assertCompactionEnabled(t *testing.T, settings map[string]any) {
	t.Helper()
	compaction, _ := settings["compaction"].(map[string]any)
	if compaction == nil || compaction["enabled"] != true {
		t.Errorf("compaction.enabled: got %v, want true", compaction)
	}
}

// assertPackages asserts that settings["packages"] contains wantPkg as its first element.
func assertPackages(t *testing.T, settings map[string]any, wantPkg string) {
	t.Helper()
	pkgs, _ := settings["packages"].([]any)
	if len(pkgs) == 0 || pkgs[0] != wantPkg {
		t.Errorf("packages: got %v, want [%s]", pkgs, wantPkg)
	}
}

// assertSymlinks checks that each name in gDir is a symlink pointing to localDir/name.
func assertSymlinks(t *testing.T, gDir, localDir string, names ...string) {
	t.Helper()
	for _, name := range names {
		target, err := os.Readlink(filepath.Join(gDir, name))
		if err != nil {
			t.Errorf("%s: symlink error: %v", name, err)
			continue
		}
		want := filepath.Join(localDir, name)
		if target != want {
			t.Errorf("%s: symlink target got %q, want %q", name, target, want)
		}
	}
}

// snapshotDir records the name → modification-time mapping for all direct entries in dir.
func snapshotDir(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	result := map[string]int64{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %q: %v", dir, err)
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatalf("Info %q: %v", e.Name(), err)
		}
		result[e.Name()] = fi.ModTime().UnixNano()
	}
	return result
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
