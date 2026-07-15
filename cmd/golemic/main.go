package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: golemic <command>\n\n")
		fmt.Fprintf(os.Stderr, "Available commands:\n")
		fmt.Fprintf(os.Stderr, "  preflight     Check prerequisites (not implemented)\n")
		fmt.Fprintf(os.Stderr, "  run           Run the main process (not implemented)\n")
		fmt.Fprintf(os.Stderr, "  emit          Emit output (not implemented)\n")
		fmt.Fprintf(os.Stderr, "  open-pr       Open a pull request (not implemented)\n")
		fmt.Fprintf(os.Stderr, "  submit-review Submit a review (not implemented)\n")
	}

	flag.Parse()

	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "preflight":
		fmt.Println("not implemented")
		os.Exit(1)
	case "run":
		fmt.Println("not implemented")
		os.Exit(1)
	case "emit":
		fmt.Println("not implemented")
		os.Exit(1)
	case "open-pr":
		fmt.Println("not implemented")
		os.Exit(1)
	case "submit-review":
		fmt.Println("not implemented")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}
}