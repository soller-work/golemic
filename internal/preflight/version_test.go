package preflight

import (
	"fmt"
	"strings"
	"testing"
)

func TestCheckGhVersion(t *testing.T) {
	tests := []struct {
		name       string
		setupExec  func() *fakeExecutor
		wantOk     bool
		wantDetail string
	}{
		{
			name: "gh installed",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "gh version 2.0.0", nil
					},
				}
			},
			wantOk: true,
		},
		{
			name: "gh not found",
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
			name: "gh exits with error",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", &ErrExit{ExitCode: 1, Stderr: "some error"}
					},
				}
			},
			wantOk:     false,
			wantDetail: "exited with code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := setupPreflight(t, tt.setupExec())
			result := p.checkGhVersion()
			if result.Ok != tt.wantOk {
				t.Errorf("checkGhVersion().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkGhVersion().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}
		})
	}
}

func TestCheckPiVersion(t *testing.T) {
	tests := []struct {
		name       string
		setupExec  func() *fakeExecutor
		wantOk     bool
		wantDetail string
	}{
		{
			name: "pi installed",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "pi version 1.0.0", nil
					},
				}
			},
			wantOk: true,
		},
		{
			name: "pi not found",
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
			name: "pi exits with error",
			setupExec: func() *fakeExecutor {
				return &fakeExecutor{
					runFunc: func(name string, args ...string) (string, error) {
						return "", &ErrExit{ExitCode: 1, Stderr: "some error"}
					},
				}
			},
			wantOk:     false,
			wantDetail: "exited with code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, _ := setupPreflight(t, tt.setupExec())
			result := p.checkPiVersion()
			if result.Ok != tt.wantOk {
				t.Errorf("checkPiVersion().Ok = %v, want %v; details: %s", result.Ok, tt.wantOk, result.Details)
			}
			if !tt.wantOk && tt.wantDetail != "" && !strings.Contains(result.Details, tt.wantDetail) {
				t.Errorf("checkPiVersion().Details = %q, want to contain %q", result.Details, tt.wantDetail)
			}
		})
	}
}
