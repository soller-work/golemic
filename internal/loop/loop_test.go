package loop

import (
	"errors"
	"testing"
)

type testCtx struct{}

func TestMachine_AC1_RunsToTerminal(t *testing.T) {
	m := Machine[testCtx]{
		Start: "A",
		Terminals: map[StepKey]bool{
			"C": true,
		},
		Handlers: map[StepKey]func(*testCtx) EventKey{
			"A": func(_ *testCtx) EventKey { return "go" },
			"B": func(_ *testCtx) EventKey { return "go" },
		},
		Transitions: []Transition[testCtx]{
			{From: "A", Event: "go", To: "B"},
			{From: "B", Event: "go", To: "C"},
		},
	}
	got, err := m.Run(&testCtx{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "C" {
		t.Errorf("got step %q, want %q", got, "C")
	}
}

func TestMachine_AC2_NoMatchingTransition(t *testing.T) {
	m := Machine[testCtx]{
		Start: "A",
		Terminals: map[StepKey]bool{
			"B": true,
		},
		Handlers: map[StepKey]func(*testCtx) EventKey{
			"A": func(_ *testCtx) EventKey { return "unknown" },
		},
		Transitions: []Transition[testCtx]{
			{From: "A", Event: "go", To: "B"},
		},
	}
	step, err := m.Run(&testCtx{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *StateError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StateError, got %T", err)
	}
	if se.Step != "A" {
		t.Errorf("StateError.Step = %q, want %q", se.Step, "A")
	}
	if se.Event != "unknown" {
		t.Errorf("StateError.Event = %q, want %q", se.Event, "unknown")
	}
	if step != "A" {
		t.Errorf("returned step = %q, want %q", step, "A")
	}
}

func TestMachine_AC3_TwoTransitionsMatch(t *testing.T) {
	m := Machine[testCtx]{
		Start: "A",
		Terminals: map[StepKey]bool{
			"B": true,
			"C": true,
		},
		Handlers: map[StepKey]func(*testCtx) EventKey{
			"A": func(_ *testCtx) EventKey { return "go" },
		},
		Transitions: []Transition[testCtx]{
			{From: "A", Event: "go", To: "B"},
			{From: "A", Event: "go", To: "C"},
		},
	}
	step, err := m.Run(&testCtx{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *StateError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StateError, got %T", err)
	}
	if se.Step != "A" || se.Event != "go" {
		t.Errorf("StateError = {%q, %q}, want {%q, %q}", se.Step, se.Event, "A", "go")
	}
	if step != "A" {
		t.Errorf("returned step = %q, want %q", step, "A")
	}
}

func TestMachine_AC4_NonTerminalWithoutHandler(t *testing.T) {
	m := Machine[testCtx]{
		Start:     "A",
		Terminals: map[StepKey]bool{},
		Handlers:  map[StepKey]func(*testCtx) EventKey{},
	}
	step, err := m.Run(&testCtx{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var se *StateError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StateError, got %T", err)
	}
	if se.Step != "A" {
		t.Errorf("StateError.Step = %q, want %q", se.Step, "A")
	}
	if step != "A" {
		t.Errorf("returned step = %q, want %q", step, "A")
	}
}
