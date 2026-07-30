package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildDetagent builds the detagent binary into a temp dir and returns its path.
func buildDetagent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build detagent: %v\n%s", err, out)
	}
	return bin
}

func TestDetagentVersion(t *testing.T) {
	bin := buildDetagent(t)

	cmd := exec.Command(bin, "--version")
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pi --version: want exit 0, got %v", err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		t.Fatal("pi --version: want non-empty version string, got empty")
	}
	t.Logf("version output: %q", line)
}

func TestDetagentUnknownRoleExitsNonZero(t *testing.T) {
	bin := buildDetagent(t)

	cmd := exec.Command(bin)
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"GOLEMIC_ROLE=unknown-role",
	}
	err := cmd.Run()
	if err == nil {
		t.Fatal("want non-zero exit for unknown GOLEMIC_ROLE, got exit 0")
	}
}
