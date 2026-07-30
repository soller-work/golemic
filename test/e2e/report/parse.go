package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type failureClass string

const (
	classPanic     failureClass = "panic"
	classSetup     failureClass = "setup"
	classAssertion failureClass = "assertion"
)

type testEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
	Output string `json:"Output"`
}

type failedScenario struct {
	Name        string
	Class       failureClass
	Fingerprint string
	Output      []string
}

// parseFailures reads go test -json output and returns the set of top-level
// tests that failed, with their collected output lines.
// Malformed JSON lines are skipped; zero parsed events is an error.
func parseFailures(r io.Reader) ([]failedScenario, error) {
	outputs, failed, parsed := scanEvents(r)
	if parsed == 0 {
		return nil, fmt.Errorf("no test events parsed — input may be empty or malformed")
	}
	result := make([]failedScenario, 0, len(failed))
	for name := range failed {
		lines := gatherOutput(name, outputs)
		cls := classify(lines)
		fp := fingerprint(name, cls)
		result = append(result, failedScenario{
			Name:        name,
			Class:       cls,
			Fingerprint: fp,
			Output:      lines,
		})
	}
	return result, nil
}

func scanEvents(r io.Reader) (outputs map[string][]string, failed map[string]bool, parsed int) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	outputs = map[string][]string{}
	failed = map[string]bool{}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip malformed lines
		}
		parsed++
		if ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "output":
			outputs[ev.Test] = append(outputs[ev.Test], ev.Output)
		case "fail":
			if !strings.Contains(ev.Test, "/") {
				failed[ev.Test] = true
			}
		}
	}
	return
}

func gatherOutput(name string, outputs map[string][]string) []string {
	var lines []string
	for k, v := range outputs {
		if k == name || strings.HasPrefix(k, name+"/") {
			lines = append(lines, v...)
		}
	}
	return lines
}

func classify(lines []string) failureClass {
	for _, l := range lines {
		if strings.Contains(l, "panic:") || strings.Contains(l, "runtime error:") {
			return classPanic
		}
	}
	for _, l := range lines {
		lower := strings.ToLower(l)
		if strings.Contains(lower, "--- skip") || strings.Contains(l, "missing prerequisite") {
			return classSetup
		}
	}
	return classAssertion
}

// fingerprint returns the first 12 hex chars of sha256("scenario|class").
func fingerprint(scenario string, cls failureClass) string {
	h := sha256.Sum256([]byte(scenario + "|" + string(cls)))
	return fmt.Sprintf("%x", h[:6])
}
