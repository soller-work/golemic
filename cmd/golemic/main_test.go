package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantExit       int
		wantStdoutSub  string // empty means stdout must be empty
		wantStderrSubs []string
	}{
		{
			name:           "no arguments prints usage to stderr",
			args:           []string{"golemic"},
			wantExit:       1,
			wantStderrSubs: []string{"Usage: golemic"},
		},
		{
			name:           "unknown command prints error to stderr",
			args:           []string{"golemic", "does-not-exist"},
			wantExit:       1,
			wantStderrSubs: []string{"Unknown command: does-not-exist", "Usage: golemic"},
		},
		{
			name:           "preflight not implemented",
			args:           []string{"golemic", "preflight"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
		{
			name:           "run not implemented",
			args:           []string{"golemic", "run"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
		{
			name:           "emit not implemented",
			args:           []string{"golemic", "emit"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
		{
			name:           "open-pr not implemented",
			args:           []string{"golemic", "open-pr"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
		{
			name:           "submit-review not implemented",
			args:           []string{"golemic", "submit-review"},
			wantExit:       1,
			wantStderrSubs: []string{"not implemented"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tc.args, &stdout, &stderr)
			if got != tc.wantExit {
				t.Errorf("exit code: got %d, want %d", got, tc.wantExit)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout must be empty for error states, got: %q", stdout.String())
			}
			for _, sub := range tc.wantStderrSubs {
				if !strings.Contains(stderr.String(), sub) {
					t.Errorf("stderr missing %q; got: %q", sub, stderr.String())
				}
			}
		})
	}
}
