package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golemic/internal/config"
	"golemic/internal/credentials"
	"golemic/internal/preflight"
	"golemic/internal/repo"
	"golemic/internal/runloop"
	"golemic/internal/runner"
)

var knownCommands = []struct {
	name string
	desc string
}{
	{"preflight", "Check prerequisites"},
	{"run", "Run the main process (golemic run --issue N)"},
	{"status", "Show run health status"},
	{"next-issue", "Return the next takeable GitHub issue (JSON)"},
	{"run-loop", "Run the autonomous 60-second polling loop for takeable issues"},
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage: golemic <command>\n\n")
	fmt.Fprintf(w, "Available commands:\n")
	for _, c := range knownCommands {
		fmt.Fprintf(w, "  %-13s %s\n", c.name, c.desc)
	}
}

// run dispatches subcommands. All error and usage output goes to stderr.
// stdout is left untouched for error states. Returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		usage(stderr)
		return 1
	}

	command := args[1]

	if command == "preflight" {
		return dispatchPreflight(args, stdout, stderr)
	}

	if code, ok := dispatchCoreCommands(command, args, stdout, stderr); ok {
		return code
	}

	if code, ok := dispatchExtendedCommands(command, args, stdout, stderr); ok {
		return code
	}

	if command == "run-loop" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return runRunLoop(ctx, args, stdout, stderr, osRunLoopExecutor{})
	}

	for _, c := range knownCommands {
		if c.name == command {
			fmt.Fprintln(stderr, "not implemented")
			return 1
		}
	}

	fmt.Fprintf(stderr, "Unknown command: %s\n", command)
	usage(stderr)
	return 1
}

// dispatchPreflight handles the preflight subcommand with its inline flag parsing.
func dispatchPreflight(args []string, stdout, stderr io.Writer) int {
	pfs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	pfs.SetOutput(stderr)
	var checkFlag bool
	pfs.BoolVar(&checkFlag, "check", false, "Run in read-only check mode (no scaffolding, local token comparison)")
	if err := pfs.Parse(args[2:]); err != nil {
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get current directory: %v\n", err)
		return 1
	}

	repoRoot, err := repo.ResolveHostRepo(osExecutor{}, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "failed to resolve host repo: %v\n", err)
		return 1
	}

	return runPreflight(osExecutor{}, homeDir, repoRoot, stdout, stderr, checkFlag)
}

// dispatchCoreCommands handles the primary subcommands.
func dispatchCoreCommands(command string, args []string, stdout, stderr io.Writer) (int, bool) {
	switch command {
	case "run":
		return runRun(args, stdout, stderr), true
	}
	return 0, false
}

// dispatchExtendedCommands handles the remaining non-loop subcommands.
func dispatchExtendedCommands(command string, args []string, stdout, stderr io.Writer) (int, bool) {
	switch command {
	case "status":
		return runStatus(args, stdout, stderr, osExecutor{}), true
	case "next-issue":
		return runNextIssue(args, stdout, stderr, osExecutor{}), true
	}
	return 0, false
}

// runPreflight executes the preflight command with injectable dependencies.
// checkMode=false runs setup mode (scaffolds); checkMode=true runs read-only check mode.
func runPreflight(executor preflight.Executor, homeDir, repoRoot string, stdout, stderr io.Writer, checkMode bool) int {
	p := preflight.New(executor, homeDir, repoRoot)
	p.SetStdout(stdout)

	var results preflight.Results
	if checkMode {
		results = p.Check()
	} else {
		results = p.RunAll()
	}

	if results.AllOK() {
		return 0
	}
	return 1
}

func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var issueNum int
	var cleanFlag bool
	var quietFlag bool
	var resumeFlag bool
	fs.IntVar(&issueNum, "issue", 0, "GitHub issue number (required)")
	fs.BoolVar(&cleanFlag, "clean", false, "Remove leftover artifacts for the issue before running")
	fs.BoolVar(&quietFlag, "quiet", false, "Suppress the run-setup header")
	fs.BoolVar(&quietFlag, "q", false, "Suppress the run-setup header (shorthand)")
	fs.BoolVar(&resumeFlag, "resume", false, "Resume from an existing open PR (skips collision check)")

	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	if issueNum <= 0 {
		fmt.Fprintln(stderr, "--issue must be a positive integer")
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get home directory: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "failed to get current directory: %v\n", err)
		return 1
	}

	r := runner.New(osExecutor{}, homeDir, cwd, issueNum)
	r.SetStdout(stdout)
	r.SetStderr(stderr)
	r.SetClean(cleanFlag)
	r.SetQuiet(quietFlag)
	r.SetResume(resumeFlag)
	return r.Run()
}

// osExecutor is the production executor that runs real commands.
type osExecutor struct{}

func (e osExecutor) Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String()}
		}
		return "", err
	}
	return stdout.String(), nil
}

func (e osExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), "GOLEMIC_GH_AUTHORIZED=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stderr: string(exitErr.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

func (e osExecutor) RunInDir(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stderr: string(exitErr.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

func (e osExecutor) RunWithEnvInDir(env map[string]string, dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOLEMIC_GH_AUTHORIZED=1")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &preflight.ErrExit{ExitCode: exitErr.ExitCode(), Stderr: string(exitErr.Stderr)}
		}
		return "", err
	}
	return string(out), nil
}

// runRunLoop executes the run-loop subcommand. It resolves the host repo,
// loads config and credentials, verifies preflight labels, then runs the
// autonomous tick loop until ctx is cancelled (SIGINT or SIGTERM in production).
func runRunLoop(ctx context.Context, _ []string, _, stderr io.Writer, executor runloop.Executor) int {
	golemicBin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	repoRoot, err := repo.ResolveHostRepo(executor, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	if _, err := credentials.NewLoader(homeDir).Load(cfg.Project); err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: %v\n", err)
		return 1
	}

	if _, err := executor.RunInDir(repoRoot, golemicBin, "preflight", "--check"); err != nil {
		fmt.Fprintf(stderr, "run-loop startup failed: preflight check: %v\n", err)
		return 1
	}

	l := runloop.New(executor, golemicBin, homeDir, repoRoot, cfg.Project, stderr)
	l.Run(ctx)
	return 0
}

// osRunLoopExecutor wraps osExecutor and adds subprocess lifecycle support for
// the runner via StartWithEnvInDir.
type osRunLoopExecutor struct {
	osExecutor
}

// osProcessHandle wraps an exec.Cmd and implements runloop.ProcessHandle.
type osProcessHandle struct {
	cmd *exec.Cmd
}

func (h *osProcessHandle) Wait() error                { return h.cmd.Wait() }
func (h *osProcessHandle) Signal(sig os.Signal) error { return h.cmd.Process.Signal(sig) }

func (e osRunLoopExecutor) StartWithEnvInDir(env map[string]string, dir, name string, args ...string) (runloop.ProcessHandle, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcessHandle{cmd: cmd}, nil
}

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}
