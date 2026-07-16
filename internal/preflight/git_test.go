package preflight

import (
	"fmt"
	"strings"
	"testing"
)

func TestCheckGit(t *testing.T) { //nolint:cyclop,funlen,gocognit // moved verbatim; cyclomatic 34 and cognitive 38 exceed thresholds on the pre-existing table body
	tests := []struct {
		name       string
		setupExec  func() *fakeExecutor
		wantOk     bool
		wantDetail string
	}{
		{
			name: "git ok with HTTPS remote",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						switch {
						case name == "git" && len(args) >= 1 && args[0] == "config":
							return "https://github.com/owner/repo.git", nil
						case name == "git" && len(args) >= 1 && args[0] == "worktree":
							return "/tmp/repo (main)\n", nil
						default:
							return "git version 2.0.0", nil
						}
					},
				}
			},
			wantOk: true,
		},
		{
			name: "git not found",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", fmt.Errorf("executable file not found")
					},
				}
			},
			wantOk:     false,
			wantDetail: "not found",
		},
		{
			name: "git worktree list fails",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						if callCount == 1 {
							return "git version 2.0.0", nil // git --version ok
						}
						return "", &ErrExit{ExitCode: 128, Stderr: "fatal: not a git repository"} // worktree fails
					},
				}
			},
			wantOk:     false,
			wantDetail: "git worktree list failed",
		},
		{
			name: "no remote origin",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "", &ErrExit{ExitCode: 1, Stderr: "fatal: not in a git directory"}
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "no remote 'origin'",
		},
		{
			name: "SSH remote URL (git@)",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "git@github.com:owner/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "SSH",
		},
		{
			name: "SSH remote URL (ssh://)",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "ssh://git@github.com/owner/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "SSH",
		},
		{
			name: "SSH remote URL (git+ssh://)",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "git+ssh://git@github.com/owner/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "SSH",
		},
		{
			name: "non-HTTPS, non-SSH URL",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "file:///local/repo.git", nil
						}
					},
				}
			},
			wantOk:     false,
			wantDetail: "HTTPS",
		},
		{
			name: "HTTPS URL with embedded token passes",
			setupExec: func() *fakeExecutor {
				callCount := 0
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						callCount++
						switch callCount {
						case 1:
							return "git version 2.0.0", nil
						case 2:
							return "/tmp/repo (main)\n", nil
						default:
							return "https://x-access-token:ghp_secret123@github.com/owner/repo.git", nil
						}
					},
				}
			},
			wantOk: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := setupPreflight(t, tt.setupExec())
			result := p.checkGit()
			if result.Ok != tt.wantOk {
				t.Errorf("checkGit().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkGit().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}

			// Verify no token in error output
			if !result.Ok && strings.Contains(result.Details, "ghp_") {
				t.Errorf("error message must not contain token values, got: %q", result.Details)
			}
		})
	}
}

func TestCheckGitTokenLeak(t *testing.T) { //nolint:cyclop // moved verbatim; cyclomatic complexity 11 exceeds threshold
	// Special test: ensure that a token in an HTTPS URL with credentials
	// is masked in the error output, and that a plain HTTPS URL with token
	// still passes.
	exec := &fakeExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			switch {
			case name == "git" && len(args) >= 1 && args[0] == "config":
				return "https://x-access-token:ghp_my_secret@github.com/owner/repo.git", nil
			case name == "git" && len(args) >= 1 && args[0] == "worktree":
				return "/tmp/repo (main)\n", nil
			default:
				return "git version 2.0.0", nil
			}
		},
	}
	p, _, _ := setupPreflight(t, exec)
	result := p.checkGit()
	if !result.Ok {
		t.Errorf("HTTPS URL with embedded token should pass, got FEHLT: %s", result.Details)
	}
	if result.Ok && strings.Contains(result.Details, "ghp_") {
		t.Errorf("error message must not contain token values, got: %q", result.Details)
	}
}

func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://github.com/owner/repo.git", false},
		{"git@github.com:owner/repo.git", true},
		{"ssh://git@github.com/owner/repo.git", true},
		{"git://github.com/owner/repo.git", true},
		{"git+ssh://git@github.com/owner/repo.git", true},
		{"http://github.com/owner/repo.git", false},
		{"file:///local/repo.git", false},
		{"", false},
		{"https://x-access-token:secret@github.com/owner/repo.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := isSSHURL(tt.url)
			if got != tt.want {
				t.Errorf("isSSHURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestMaskURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"https://user:pass@github.com/owner/repo.git", "https://***@github.com/owner/repo.git"},
		{"git@github.com:owner/repo.git", "git@github.com:owner/repo.git"},
		{"https://token@github.com/owner/repo.git", "https://***@github.com/owner/repo.git"},
		{"https://x-access-token:ghp_secret@github.com/owner/repo.git", "https://***@github.com/owner/repo.git"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := maskURL(tt.url)
			if got != tt.want {
				t.Errorf("maskURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AC-003: checkGit pins worktree list and config to p.repoRoot
// ---------------------------------------------------------------------------

// dirCapturingExecutor records dirs used by RunInDir.
type dirCapturingExecutor struct {
	dirs     []string
	runFunc  func(name string, args ...string) (string, error)
}

func (d *dirCapturingExecutor) Run(name string, args ...string) (string, error) {
	if d.runFunc != nil {
		return d.runFunc(name, args...)
	}
	return "", nil
}

func (d *dirCapturingExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	return d.Run(name, args...)
}

func (d *dirCapturingExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	d.dirs = append(d.dirs, dir)
	if d.runFunc != nil {
		return d.runFunc(name, args...)
	}
	return "", nil
}

func (d *dirCapturingExecutor) RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error) {
	return d.RunInDir(dir, name, args...)
}

func TestCheckGit_PinnedToRepoRoot_AC003(t *testing.T) {
	const repoRoot = "/fake/host/repo"

	callIdx := 0
	exec := &dirCapturingExecutor{
		runFunc: func(name string, args ...string) (string, error) {
			callIdx++
			switch callIdx {
			case 1:
				return "git version 2.0.0", nil // git --version (not pinned)
			case 2:
				return "/fake/host/repo (main)\n", nil // git worktree list
			default:
				return "https://github.com/owner/repo.git", nil // git config
			}
		},
	}

	p := New(exec, t.TempDir(), repoRoot)
	result := p.checkGit()

	if !result.Ok {
		t.Fatalf("checkGit() unexpectedly failed: %s", result.Details)
	}

	// Both pinned calls must use repoRoot
	if len(exec.dirs) < 2 {
		t.Fatalf("expected at least 2 RunInDir calls, got %d", len(exec.dirs))
	}
	for i, dir := range exec.dirs {
		if dir != repoRoot {
			t.Errorf("RunInDir call %d used dir %q, want %q", i, dir, repoRoot)
		}
	}
}
