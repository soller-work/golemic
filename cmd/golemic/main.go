package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golemic/internal/preflight"
)

var knownCommands = []struct {
	name string
	desc string
}{
	{"preflight", "Check prerequisites"},
	{"run", "Run the main process (not implemented)"},
	{"emit", "Emit output (not implemented)"},
	{"open-pr", "Open a pull request (not implemented)"},
	{"submit-review", "Submit a review (not implemented)"},
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

	// Special case: preflight is implemented
	if command == "preflight" {
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

		// Try to find git root; fall back to cwd
		gitRoot, err := osExecutor{}.Run("git", "rev-parse", "--show-toplevel")
		repoRoot := cwd
		if err == nil && gitRoot != "" {
			repoRoot = strings.TrimSpace(gitRoot)
		}

		return runPreflight(osExecutor{}, homeDir, repoRoot, stdout, stderr)
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

// runPreflight executes the preflight command with injectable dependencies.
// All external effects (executor, homeDir, repoRoot) are parameters so tests
// can use fakes and temp directories.
func runPreflight(executor preflight.Executor, homeDir, repoRoot string, stdout, stderr io.Writer) int {

	p := preflight.New(executor, homeDir, repoRoot)
	p.SetStdout(stdout)

	results := p.RunAll()

	if results.AllOK() {
		fmt.Fprintln(stdout, "SUCCESS")
		return 0
	}
	return 1
}

// osExecutor is the production executor that runs real commands.
type osExecutor struct{}

func (e osExecutor) Run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
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

func (e osExecutor) RunWithEnv(env map[string]string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
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

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}
