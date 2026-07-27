package loop

import "testing"

// TestStepLabels_Totality asserts every AllSteps() key has a label.
func TestStepLabels_Totality(t *testing.T) {
	for _, k := range AllSteps() {
		if _, ok := StepLabel(k); !ok {
			t.Errorf("AllSteps() key %q has no label in stepLabels", k)
		}
	}
}

// TestEventLabels_Totality asserts every AllEvents() key has a label.
func TestEventLabels_Totality(t *testing.T) {
	for _, k := range AllEvents() {
		if _, ok := EventLabel(k); !ok {
			t.Errorf("AllEvents() key %q has no label in eventLabels", k)
		}
	}
}

// TestStepLabels_NoOrphans asserts stepLabels contains no keys absent from AllSteps().
func TestStepLabels_NoOrphans(t *testing.T) {
	known := make(map[StepKey]bool, len(AllSteps()))
	for _, k := range AllSteps() {
		known[k] = true
	}
	for k := range stepLabels {
		if !known[k] {
			t.Errorf("stepLabels has orphan key %q not in AllSteps()", k)
		}
	}
}

// TestEventLabels_NoOrphans asserts eventLabels contains no keys absent from AllEvents().
func TestEventLabels_NoOrphans(t *testing.T) {
	known := make(map[EventKey]bool, len(AllEvents()))
	for _, k := range AllEvents() {
		known[k] = true
	}
	for k := range eventLabels {
		if !known[k] {
			t.Errorf("eventLabels has orphan key %q not in AllEvents()", k)
		}
	}
}
