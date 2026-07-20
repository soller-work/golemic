package runner

import (
	"fmt"
	"io"
	"path/filepath"
	"time"
)

// writeRunHeader writes the run-setup header to w. Must only be called after
// r.issue is set. Stderr write errors are ignored (consistent with other
// fmt.Fprintf(r.stderr, ...) call sites in runner.go).
func (r *Runner) writeRunHeader(w io.Writer) {
	runsDir := filepath.Join(r.homeDir, ".golemic", r.project, "runs", r.runID)
	eventLogPath := filepath.Join(runsDir, "events.jsonl")

	devWorktree := filepath.Join(r.homeDir, ".golemic", r.project, "worktrees", fmt.Sprintf("issue-%d", r.issueNum))
	reviewerWorktree := filepath.Join(r.homeDir, ".golemic", r.project, "worktrees", fmt.Sprintf("issue-%d-review", r.issueNum))

	var timeout time.Duration
	if r.cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(r.cfg.TimeoutSeconds) * time.Second
	} else {
		timeout = time.Duration(r.cfg.TimeoutMinutes) * time.Minute
	}

	fmt.Fprintln(w, "golemic run")                                                                                //nolint:errcheck
	fmt.Fprintf(w, "Issue:          #%d %s\n", r.issue.Number, r.issue.Title)                                    //nolint:errcheck
	fmt.Fprintf(w, "Project:        %s\n", r.project)                                                            //nolint:errcheck
	fmt.Fprintf(w, "Run ID:         %s\n", r.runID)                                                              //nolint:errcheck
	fmt.Fprintf(w, "Models:         dev=%s  reviewer=%s\n", r.cfg.Models.Dev, r.cfg.Models.Reviewer)             //nolint:errcheck
	fmt.Fprintf(w, "Branch:         %s\n", r.branchName)                                                         //nolint:errcheck
	fmt.Fprintf(w, "Timeout:        %s\n", timeout)                                                              //nolint:errcheck
	fmt.Fprintf(w, "Event log:      %s\n", eventLogPath)                                                         //nolint:errcheck
	fmt.Fprintf(w, "Dev logs:       %s\n", filepath.Join(runsDir, "dev.activity.jsonl"))                         //nolint:errcheck
	fmt.Fprintf(w, "                %s\n", filepath.Join(runsDir, "dev.stderr.log"))                             //nolint:errcheck
	fmt.Fprintf(w, "Reviewer logs:  %s\n", filepath.Join(runsDir, "reviewer.activity.jsonl"))                     //nolint:errcheck
	fmt.Fprintf(w, "                %s\n", filepath.Join(runsDir, "reviewer.stderr.log"))                        //nolint:errcheck
	fmt.Fprintf(w, "\nTip: tail -f %s\n", filepath.Join(runsDir, "dev.activity.jsonl"))                           //nolint:errcheck
	fmt.Fprintf(w, "     tail -f %s\n", filepath.Join(runsDir, "reviewer.activity.jsonl"))                       //nolint:errcheck
	fmt.Fprintf(w, "Dev worktree:   %s\n", devWorktree)                                                          //nolint:errcheck
	fmt.Fprintf(w, "Rev worktree:   %s\n", reviewerWorktree)                                                     //nolint:errcheck
	fmt.Fprintf(w, "\nTip: tail -f %s\n", eventLogPath)                                                          //nolint:errcheck
	fmt.Fprintln(w)                                                                                               //nolint:errcheck
}
