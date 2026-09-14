package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// The per-message log is the run's readable log: one line per llm_messages row
// (user input, thinking block, tool call + result, assistant return), written
// when the row is persisted. Raw stream events (llm_events) are a replay detail
// and stay out of the log — the provider bridges keep their own opt-in traces.

// llmMessageLogDisabled lists the AUTONOMY_LLM_TRACE values that silence the
// per-message log. Anything else (including unset) keeps it on.
var llmMessageLogDisabled = map[string]bool{
	"0": true, "false": true, "off": true, "no": true, "silent": true, "quiet": true, "none": true,
}

// llmMessageLogDefaultMax caps one logged field by default (AUTONOMY_LLM_TRACE_MAX
// overrides it): a thinking block or a tool result can be long, and the line
// stays readable while still saying how much it cut.
const llmMessageLogDefaultMax = 500

func llmMessageLogEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AUTONOMY_LLM_TRACE")))
	return !llmMessageLogDisabled[v]
}

func llmMessageLogMax() int {
	raw := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_TRACE_MAX"))
	if raw == "" {
		return llmMessageLogDefaultMax
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return llmMessageLogDefaultMax
	}
	return n
}

// logLLMMessage mirrors one stored conversation message to stderr.
func logLLMMessage(m LLMMessage) {
	if !llmMessageLogEnabled() {
		return
	}
	fmt.Fprintln(os.Stderr, formatLLMMessage(m))
}

// formatLLMMessage renders one llm_messages row as a log line, with the
// role-specific detail a reader needs: the tool's name/call/args next to its
// result, and how long the model thought.
func formatLLMMessage(m LLMMessage) string {
	max := llmMessageLogMax()
	switch m.Role {
	case LLMMessageRoleUser:
		return fmt.Sprintf("[autonomy] llm seq=%d user: %s", m.Seq, condenseLLMText(m.Content, max))
	case LLMMessageRoleThinking:
		return fmt.Sprintf("[autonomy] llm seq=%d thinking%s: %s",
			m.Seq, thinkingDurationLabel(m), condenseLLMText(m.Content, max))
	case LLMMessageRoleTool:
		return fmt.Sprintf("[autonomy] llm seq=%d tool %s -> %s",
			m.Seq, toolCallLabel(m, max), condenseLLMText(m.Content, max))
	default:
		return fmt.Sprintf("[autonomy] llm seq=%d %s: %s", m.Seq, m.Role, condenseLLMText(m.Content, max))
	}
}

// toolCallLabel renders a tool row's normalized_content (name / call_id / args)
// as key=value pairs.
func toolCallLabel(m LLMMessage, max int) string {
	var n struct {
		Name   string          `json:"name"`
		CallID string          `json:"call_id"`
		Args   json.RawMessage `json:"args"`
	}
	parts := []string{}
	if m.NormalizedContent != "" && json.Unmarshal([]byte(m.NormalizedContent), &n) == nil {
		if n.Name != "" {
			parts = append(parts, n.Name)
		}
		if n.CallID != "" {
			parts = append(parts, "call="+n.CallID)
		}
		if len(n.Args) > 0 && string(n.Args) != "null" {
			parts = append(parts, "args="+condenseLLMText(string(n.Args), max))
		}
	}
	if len(parts) == 0 {
		return "tool"
	}
	return strings.Join(parts, " ")
}

// thinkingDurationLabel renders a thinking row's derived duration (Cursor reports
// it, Cline's is measured from the block's event span) as " (1.8s)".
func thinkingDurationLabel(m LLMMessage) string {
	var n struct {
		DurationMS int64 `json:"duration_ms"`
	}
	if m.NormalizedContent == "" || json.Unmarshal([]byte(m.NormalizedContent), &n) != nil || n.DurationMS <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%s)", time.Duration(n.DurationMS)*time.Millisecond)
}

// condenseLLMText collapses a message's whitespace so it fits on one log line
// (thinking is prose with line breaks), and cuts it at max runes with a marker
// naming how much was dropped.
func condenseLLMText(text string, max int) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= max {
		return flat
	}
	return fmt.Sprintf("%s…(+%dch)", string(runes[:max]), len(runes)-max)
}
