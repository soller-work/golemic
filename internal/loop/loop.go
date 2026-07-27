package loop

import "fmt"

// StepKey names a step (what to do next).
type StepKey string

// EventKey names an event (fact produced by a step handler).
type EventKey string

// Transition is one edge in the machine. Guard nil means unconditional.
type Transition[C any] struct {
	From  StepKey
	Event EventKey
	To    StepKey
	Guard func(*C) bool
}

// TransitionObserver observes every successful state transition.
type TransitionObserver[C any] func(from StepKey, event EventKey, to StepKey, guarded bool)

// StateError is raised when dispatch cannot continue.
type StateError struct {
	Step  StepKey
	Event EventKey
	Msg   string
}

func (e *StateError) Error() string {
	return fmt.Sprintf("step %s: event %s: %s", e.Step, e.Event, e.Msg)
}

// Machine is a guarded transition machine over context type C.
type Machine[C any] struct {
	Transitions  []Transition[C]
	Handlers     map[StepKey]func(*C) EventKey
	Start        StepKey
	Terminals    map[StepKey]bool
	OnTransition TransitionObserver[C]
}

// Run walks the machine from Start until a terminal step is reached.
// It returns (step, nil) on a terminal, or (step, *StateError) on:
//   - missing handler for a non-terminal step
//   - zero or more-than-one transitions match for (step, event)
func (m *Machine[C]) Run(ctx *C) (StepKey, error) {
	step := m.Start
	for {
		if m.Terminals[step] {
			return step, nil
		}
		handler, ok := m.Handlers[step]
		if !ok {
			return step, &StateError{Step: step, Msg: "no handler"}
		}
		ev := handler(ctx)
		matched := m.collectMatches(step, ev, ctx)
		if len(matched) == 1 {
			next := matched[0].To
			if m.OnTransition != nil {
				m.OnTransition(step, ev, next, matched[0].Guard != nil)
			}
			step = next
			continue
		}
		return step, &StateError{Step: step, Event: ev, Msg: dispatchErrMsg(len(matched))}
	}
}

func (m *Machine[C]) collectMatches(step StepKey, ev EventKey, ctx *C) []Transition[C] {
	var matched []Transition[C]
	for _, t := range m.Transitions {
		if t.From != step || t.Event != ev {
			continue
		}
		if t.Guard != nil && !t.Guard(ctx) {
			continue
		}
		matched = append(matched, t)
	}
	return matched
}

func dispatchErrMsg(n int) string {
	if n == 0 {
		return "no matching transition"
	}
	return fmt.Sprintf("%d transitions matched", n)
}
