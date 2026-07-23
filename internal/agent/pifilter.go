package agent

import (
	"bytes"
	"encoding/json"
	"io"
)

// lineFilterWriter buffers pi subprocess stdout and applies filterPiLine to
// each complete newline-terminated line before forwarding to dst.
type lineFilterWriter struct {
	dst    io.Writer
	buf    []byte
	onLine func([]byte)
}

func (w *lineFilterWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		out := filterPiLine(w.buf[:i+1])
		if _, err := w.dst.Write(out); err != nil {
			return 0, err
		}
		if w.onLine != nil {
			w.onLine(out)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// filterPiLine projects message_update lines to metadata + delta only; every
// other line (including malformed JSON and unrecognised events) is returned
// byte-for-byte unchanged.
func filterPiLine(line []byte) []byte {
	data := bytes.TrimRight(line, "\n")
	var ev struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &ev) != nil || ev.Type != "message_update" {
		return line
	}
	projected := projectMessageUpdate(data)
	if projected == nil {
		return line
	}
	return append(projected, '\n')
}

// muAMEInput is the assistantMessageEvent shape we read from a message_update.
type muAMEInput struct {
	Type         string          `json:"type"`
	ContentIndex json.RawMessage `json:"contentIndex"`
	Delta        json.RawMessage `json:"delta"`
}

// muInput is the top-level message_update shape we read (we ignore partial/message).
type muInput struct {
	AssistantMessageEvent muAMEInput      `json:"assistantMessageEvent"`
	Usage                 json.RawMessage `json:"usage"`
	Model                 string          `json:"model"`
	StopReason            string          `json:"stopReason"`
	Timestamp             string          `json:"timestamp"`
}

// muAMEOutput is the projected assistantMessageEvent written to disk.
type muAMEOutput struct {
	Type         string          `json:"type,omitempty"`
	ContentIndex json.RawMessage `json:"contentIndex,omitempty"`
	Delta        json.RawMessage `json:"delta,omitempty"`
}

// muOutput is the projected message_update written to disk.
type muOutput struct {
	Type                  string          `json:"type"`
	AssistantMessageEvent muAMEOutput     `json:"assistantMessageEvent"`
	Usage                 json.RawMessage `json:"usage,omitempty"`
	Model                 string          `json:"model,omitempty"`
	StopReason            string          `json:"stopReason,omitempty"`
	Timestamp             string          `json:"timestamp,omitempty"`
}

// projectMessageUpdate decodes a message_update JSON line and returns a
// projected JSON object keeping only: type, assistantMessageEvent.{type,
// contentIndex, delta}, usage, model, stopReason, timestamp. The cumulative
// assistantMessageEvent.partial.content and top-level message fields are
// dropped. Returns nil on any unmarshal/marshal failure (caller falls back to
// verbatim).
func terminalDoneFromLine(line []byte) bool {
	var ev struct {
		Type     string          `json:"type"`
		ToolName string          `json:"toolName"`
		Result   json.RawMessage `json:"result"`
	}
	if json.Unmarshal(bytes.TrimSpace(line), &ev) != nil || ev.Type != "tool_execution_end" {
		return false
	}
	if ev.ToolName == "gm_dev_done" {
		return true
	}
	if ev.ToolName != "gm_review_submit" {
		return false
	}
	var result struct {
		OK       bool `json:"ok"`
		Accepted bool `json:"accepted"`
	}
	return json.Unmarshal(ev.Result, &result) == nil && result.OK && result.Accepted
}

func projectMessageUpdate(data []byte) []byte {
	var in muInput
	if err := json.Unmarshal(data, &in); err != nil {
		return nil
	}
	out := muOutput{
		Type: "message_update",
		AssistantMessageEvent: muAMEOutput{
			Type:         in.AssistantMessageEvent.Type,
			ContentIndex: in.AssistantMessageEvent.ContentIndex,
			Delta:        in.AssistantMessageEvent.Delta,
		},
		Usage:      in.Usage,
		Model:      in.Model,
		StopReason: in.StopReason,
		Timestamp:  in.Timestamp,
	}
	result, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return result
}
