package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golemic/internal/config"
	"golemic/internal/health"
	"golemic/internal/preflight"
	"golemic/internal/repo"
)

// runStatus implements the golemic status subcommand.
func runStatus(args []string, stdout, stderr io.Writer, executor preflight.Executor) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var jsonFlag bool
	var stalledAfterFlag string
	fs.BoolVar(&jsonFlag, "json", false, "Emit JSON output")
	fs.StringVar(&stalledAfterFlag, "stalled-after", "", "Override stalled threshold (e.g. 30m)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	var runIDFilter string
	if fs.NArg() > 0 {
		runIDFilter = fs.Arg(0)
		if strings.ContainsAny(runIDFilter, "/\\") {
			fmt.Fprintln(stderr, "runId must not contain path separators") //nolint:errcheck
			return 1
		}
	}

	stalledAfter, ok := parseStalledAfter(stalledAfterFlag, stderr)
	if !ok {
		return 1
	}

	runsDir, cfgTimeout, ok := resolveRunsDir(executor, stderr)
	if !ok {
		return 1
	}

	effectiveStalledAfter := stalledAfter
	if effectiveStalledAfter == 0 {
		effectiveStalledAfter = cfgTimeout
	}

	classifier := &health.Classifier{
		Probe:        health.OsLivenessProbe,
		StalledAfter: effectiveStalledAfter,
	}

	return dispatchStatus(runIDFilter, runsDir, jsonFlag, classifier, stdout, stderr)
}

func parseStalledAfter(flag string, stderr io.Writer) (time.Duration, bool) {
	if flag == "" {
		return 0, true
	}
	d, err := time.ParseDuration(flag)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --stalled-after: %v\n", err) //nolint:errcheck
		return 0, false
	}
	return d, true
}

func resolveRunsDir(executor preflight.Executor, stderr io.Writer) (string, time.Duration, bool) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err) //nolint:errcheck
		return "", 0, false
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get current directory: %v\n", err) //nolint:errcheck
		return "", 0, false
	}
	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "failed to resolve host repo: %v\n", err) //nolint:errcheck
		return "", 0, false
	}
	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "failed to load config: %v\n", err) //nolint:errcheck
		return "", 0, false
	}
	var cfgTimeout time.Duration
	if cfg.TimeoutSeconds > 0 {
		cfgTimeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	} else {
		cfgTimeout = time.Duration(cfg.TimeoutMinutes) * time.Minute
	}
	return filepath.Join(homeDir, ".golemic", cfg.Project, "runs"), cfgTimeout, true
}

func dispatchStatus(runIDFilter, runsDir string, jsonFlag bool, classifier *health.Classifier, stdout, stderr io.Writer) int {
	if runIDFilter != "" {
		return statusSingleRun(runIDFilter, runsDir, jsonFlag, classifier, stdout, stderr)
	}
	return statusAllRuns(runsDir, jsonFlag, classifier, stdout, stderr)
}

func statusSingleRun(runIDFilter, runsDir string, jsonFlag bool, classifier *health.Classifier, stdout, stderr io.Writer) int {
	runDir := filepath.Join(runsDir, runIDFilter)
	h, err := classifier.ClassifyOne(runDir)
	if err != nil {
		if strings.HasPrefix(err.Error(), "RUN_NOT_FOUND") {
			fmt.Fprintf(stderr, "run %q not found\n", runIDFilter) //nolint:errcheck
		} else {
			fmt.Fprintf(stderr, "%v\n", err) //nolint:errcheck
		}
		return 1
	}
	if jsonFlag {
		return emitJSON(stdout, stderr, []health.RunHealth{h})
	}
	return emitTable(stdout, stderr, []health.RunHealth{h})
}

func statusAllRuns(runsDir string, jsonFlag bool, classifier *health.Classifier, stdout, stderr io.Writer) int {
	runs, err := classifier.ClassifyAll(runsDir)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err) //nolint:errcheck
		return 1
	}
	if len(runs) == 0 {
		if jsonFlag {
			fmt.Fprintln(stdout, "[]") //nolint:errcheck
		} else {
			fmt.Fprintln(stdout, "no runs found") //nolint:errcheck
		}
		return 0
	}
	if jsonFlag {
		return emitJSON(stdout, stderr, runs)
	}
	return emitTable(stdout, stderr, runs)
}

func emitJSON(stdout, stderr io.Writer, runs []health.RunHealth) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(runs); err != nil {
		fmt.Fprintf(stderr, "failed to encode JSON: %v\n", err) //nolint:errcheck
		return 1
	}
	return 0
}

func emitTable(stdout, stderr io.Writer, runs []health.RunHealth) int {
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN_ID\tISSUE\tSTATUS\tPHASE\tAGE\tOUTCOME\tPID\tLIVENESS") //nolint:errcheck
	for _, h := range runs {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
			h.RunID, h.Issue, h.Status,
			orDash(h.CurrentPhase), h.AgeOrDuration,
			orDash(h.Outcome), pidStr(h.PID), h.Liveness)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintf(stderr, "failed to write table: %v\n", err) //nolint:errcheck
		return 1
	}
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func pidStr(pid *int) string {
	if pid == nil {
		return "-"
	}
	return strconv.Itoa(*pid)
}
