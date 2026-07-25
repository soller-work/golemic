package agentfile

import (
	"strings"
	"testing"
)

func TestEmbeddedPersona_DevParsesSuccessfully(t *testing.T) {
	chain, body, err := EmbeddedPersona("dev")
	if err != nil {
		t.Fatalf("EmbeddedPersona(dev): %v", err)
	}
	if len(chain) == 0 {
		t.Error("dev persona: model chain must not be empty")
	}
	if body == "" {
		t.Error("dev persona: body must not be empty")
	}
}

func TestEmbeddedPersona_ReviewerParsesSuccessfully(t *testing.T) {
	chain, body, err := EmbeddedPersona("reviewer")
	if err != nil {
		t.Fatalf("EmbeddedPersona(reviewer): %v", err)
	}
	if len(chain) == 0 {
		t.Error("reviewer persona: model chain must not be empty")
	}
	if body == "" {
		t.Error("reviewer persona: body must not be empty")
	}
}

func TestEmbeddedPersona_UnknownRoleErrors(t *testing.T) {
	_, _, err := EmbeddedPersona("unknown")
	if err == nil {
		t.Error("expected error for unknown role")
	}
}

// Dev persona must contain the reuse ladder rungs.
func TestEmbeddedPersona_DevContainsReuseLadder(t *testing.T) {
	_, body, err := EmbeddedPersona("dev")
	if err != nil {
		t.Fatalf("EmbeddedPersona(dev): %v", err)
	}
	for _, rung := range []string{
		"YAGNI",
		"Standard library",
		"Already-installed dependency",
		"New code",
	} {
		if !strings.Contains(body, rung) {
			t.Errorf("dev persona body missing reuse ladder rung %q", rung)
		}
	}
}

// Dev persona must contain the SHORTCUT marker convention.
func TestEmbeddedPersona_DevContainsSHORTCUTMarker(t *testing.T) {
	_, body, err := EmbeddedPersona("dev")
	if err != nil {
		t.Fatalf("EmbeddedPersona(dev): %v", err)
	}
	if !strings.Contains(body, "// SHORTCUT:") {
		t.Error("dev persona body must contain the // SHORTCUT: marker convention")
	}
}

// Dev persona must retain existing safeguards: TDD workflow, BLOCKED policy, scope rules.
func TestEmbeddedPersona_DevRetainsSafeguards(t *testing.T) {
	_, body, err := EmbeddedPersona("dev")
	if err != nil {
		t.Fatalf("EmbeddedPersona(dev): %v", err)
	}
	for _, safeguard := range []string{
		"RED",
		"GREEN",
		"REFACTOR",
		"BLOCKED",
		"Scope rules",
	} {
		if !strings.Contains(body, safeguard) {
			t.Errorf("dev persona body missing safeguard section %q", safeguard)
		}
	}
}

// Reviewer persona must contain the reuse-ladder checklist item with SHORTCUT marker.
func TestEmbeddedPersona_ReviewerContainsReuseLadderChecklistItem(t *testing.T) {
	_, body, err := EmbeddedPersona("reviewer")
	if err != nil {
		t.Fatalf("EmbeddedPersona(reviewer): %v", err)
	}
	if !strings.Contains(body, "// SHORTCUT:") {
		t.Error("reviewer persona body must reference // SHORTCUT: in checklist item")
	}
	if !strings.Contains(body, "reuse ladder") {
		t.Error("reviewer persona body must contain a reuse ladder checklist item")
	}
}

// Reviewer persona must retain the severity scale and verdict rules.
func TestEmbeddedPersona_ReviewerRetainsSafeguards(t *testing.T) {
	_, body, err := EmbeddedPersona("reviewer")
	if err != nil {
		t.Fatalf("EmbeddedPersona(reviewer): %v", err)
	}
	for _, safeguard := range []string{
		"P1",
		"P2",
		"CHANGES_REQUESTED",
		"APPROVED",
		"confidence",
	} {
		if !strings.Contains(body, safeguard) {
			t.Errorf("reviewer persona body missing safeguard %q", safeguard)
		}
	}
}
