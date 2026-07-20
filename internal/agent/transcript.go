package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// piEvent is a minimal shape for Pi JSON mode events.
type piEvent struct {
	Type    string     `json:"type"`
	Success *bool      `json:"success,omitempty"` // auto_retry_end
	Message *piMessage `json:"message,omitempty"` // message_end
}

type piMessage struct {
	Role         string `json:"role"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

// TranscriptResult describes what was found in a Pi activity JSONL transcript.
type TranscriptResult struct {
	SemanticFailed   bool   // true when last assistant message_end has stopReason error or aborted
	FallbackEligible bool   // true when auto_retry_end success=false or stopReason error + "limit" in errorMessage
	Reason           string // short sanitized category for diagnostics
}

// InspectTranscript reads the Pi activity JSONL at path and classifies the run.
// Malformed lines are skipped; an unreadable file returns zero value (not semantic failure)
// to avoid false positives from missing transcripts.
func InspectTranscript(path string) TranscriptResult {
	f, err := os.Open(path)
	if err != nil {
		return TranscriptResult{}
	}
	defer f.Close() //nolint:errcheck
	return scanTranscript(f)
}

func scanTranscript(r io.Reader) TranscriptResult {
	var result TranscriptResult
	var lastStopReason, lastErrorMessage string

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var ev piEvent
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "auto_retry_end":
			applyAutoRetryEnd(&result, ev)
		case "message_end":
			if ev.Message != nil && ev.Message.Role == "assistant" {
				lastStopReason = ev.Message.StopReason
				lastErrorMessage = ev.Message.ErrorMessage
			}
		}
	}

	applyLastAssistant(&result, lastStopReason, lastErrorMessage)
	return result
}

func applyAutoRetryEnd(result *TranscriptResult, ev piEvent) {
	if ev.Success != nil && !*ev.Success {
		result.FallbackEligible = true
		if result.Reason == "" {
			result.Reason = "auto_retry_end success=false"
		}
	}
}

func applyLastAssistant(result *TranscriptResult, stopReason, errorMessage string) {
	if stopReason != "error" && stopReason != "aborted" {
		return
	}
	result.SemanticFailed = true
	if stopReason == "error" && strings.Contains(strings.ToLower(errorMessage), "limit") {
		result.FallbackEligible = true
		if result.Reason == "" {
			result.Reason = "provider limit"
		}
		return
	}
	if result.Reason == "" {
		result.Reason = "stopReason:" + stopReason
	}
}
