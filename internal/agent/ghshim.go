package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ghShimCreator creates a gh shim directory for the agent subprocess PATH.
// Override in tests to control what the "real" gh binary is.
var ghShimCreator = defaultCreateGHShim

// defaultCreateGHShim finds the real gh on the host PATH and creates a shim that
// blocks direct invocations unless GOLEMIC_GH_AUTHORIZED=1 is set in the environment.
// Returns ("", noop) if gh is not present on the host (nothing to shim).
func defaultCreateGHShim() (shimDir string, cleanup func()) {
	realGH, err := exec.LookPath("gh")
	if err != nil {
		return "", func() {}
	}
	shimDir, cleanup, err = createGHShimWithPath(realGH)
	if err != nil {
		return "", func() {}
	}
	return shimDir, cleanup
}

// createGHShimWithPath writes a gh shim script that delegates to realGHPath when
// GOLEMIC_GH_AUTHORIZED=1 is present, and fails closed otherwise. Callers must
// invoke the returned cleanup function when the shim is no longer needed.
func createGHShimWithPath(realGHPath string) (shimDir string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "golemic-ghshim-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("ghshim: create temp dir: %w", err)
	}

	script := "#!/bin/sh\n" +
		"if [ -z \"$GOLEMIC_GH_AUTHORIZED\" ]; then\n" +
		"    printf 'golemic guard: direct \"gh\" is not permitted from agent bash — use \"golemic ...\" wrapper commands instead\\n' >&2\n" +
		"    exit 1\n" +
		"fi\n" +
		"exec " + realGHPath + " \"$@\"\n"

	shimPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(shimPath, []byte(script), 0755); err != nil { //nolint:gosec
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("ghshim: write shim: %w", err)
	}

	return dir, func() { _ = os.RemoveAll(dir) }, nil
}
