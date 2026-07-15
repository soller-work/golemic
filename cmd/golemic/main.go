package main

import (
	"fmt"
	"io"
	"os"
)

var knownCommands = []struct {
	name string
	desc string
}{
	{"preflight", "Check prerequisites (not implemented)"},
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

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}
