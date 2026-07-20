package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.activity.jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectTranscript_Success(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","errorMessage":""}}`,
	)
	tr := InspectTranscript(path)
	if tr.SemanticFailed {
		t.Error("stopReason:stop should not be semantic failure")
	}
	if tr.FallbackEligible {
		t.Error("stopReason:stop should not be fallback eligible")
	}
}

func TestInspectTranscript_LimitError_FallbackEligible(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"You've hit your limit · resets 1:10pm"}}`,
	)
	tr := InspectTranscript(path)
	if !tr.SemanticFailed {
		t.Error("stopReason:error should be semantic failure")
	}
	if !tr.FallbackEligible {
		t.Error("errorMessage containing 'limit' should be fallback eligible")
	}
	if tr.Reason != "provider limit" {
		t.Errorf("reason = %q, want %q", tr.Reason, "provider limit")
	}
}

func TestInspectTranscript_ErrorWithoutLimit_NotFallbackEligible(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"tool execution failed"}}`,
	)
	tr := InspectTranscript(path)
	if !tr.SemanticFailed {
		t.Error("stopReason:error should be semantic failure")
	}
	if tr.FallbackEligible {
		t.Error("non-limit error should not be fallback eligible")
	}
}

func TestInspectTranscript_Aborted_SemanticFailure(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"aborted","errorMessage":""}}`,
	)
	tr := InspectTranscript(path)
	if !tr.SemanticFailed {
		t.Error("stopReason:aborted should be semantic failure")
	}
}

func TestInspectTranscript_AutoRetryEnd_FallbackEligible(t *testing.T) {
	f := false
	_ = f
	path := writeTranscript(t,
		`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"rate limited"}`,
	)
	tr := InspectTranscript(path)
	if !tr.FallbackEligible {
		t.Error("auto_retry_end success=false should be fallback eligible")
	}
	if tr.Reason != "auto_retry_end success=false" {
		t.Errorf("reason = %q, want %q", tr.Reason, "auto_retry_end success=false")
	}
}

func TestInspectTranscript_AutoRetrySuccess_NotFallbackEligible(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"auto_retry_end","success":true,"attempt":1}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`,
	)
	tr := InspectTranscript(path)
	if tr.SemanticFailed {
		t.Error("should not be semantic failure")
	}
	if tr.FallbackEligible {
		t.Error("auto_retry_end success=true should not be fallback eligible")
	}
}

func TestInspectTranscript_MissingFile_NoFailure(t *testing.T) {
	tr := InspectTranscript("/does/not/exist.jsonl")
	if tr.SemanticFailed {
		t.Error("missing file should not report semantic failure")
	}
	if tr.FallbackEligible {
		t.Error("missing file should not report fallback eligible")
	}
}

func TestInspectTranscript_MalformedLines_Skipped(t *testing.T) {
	path := writeTranscript(t,
		`not json at all`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`,
	)
	tr := InspectTranscript(path)
	if tr.SemanticFailed {
		t.Error("stopReason:stop should not be semantic failure despite malformed line")
	}
}

func TestInspectTranscript_LimitCaseInsensitive(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"You've hit your LIMIT"}}`,
	)
	tr := InspectTranscript(path)
	if !tr.FallbackEligible {
		t.Error("limit check should be case-insensitive")
	}
}
