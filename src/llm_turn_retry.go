package autonomy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kaulie/autonomy/src/capability/broker"
)

// A run can be cut off by the model's output-token limit instead of failing for
// a reason the session cannot fix.
//
// The Cline SDK is the one that decides this, and it decides it for the whole
// run: a turn whose finish reason is max-tokens and that produced no tool call is
// raised as a failed run ("Model reached the maximum output token limit before
// completing the turn", @cline/agents). The turn is truncated — its tool call was
// cut mid-JSON, so nothing of it ran — while the resident session is untouched:
// the conversation is intact and the next prompt works. Losing the whole
// delegation (and, upstream, the task) to one over-long turn is the wrong trade,
// so the runtime spends a bounded retry on the same session, asking for the rest
// of the work in smaller steps.
//
// The reminder is a policy file (src/agent_policy/TURN_TRUNCATED.md), not Go
// source, like every other prompt in this runtime.

// DefaultTurnRetries is how many extra turns the runtime is willing to send to a
// worker whose turn was cut off. One is enough to recover a truncated turn (the
// model is told what happened); more would hide a model that cannot work in small
// steps. AUTONOMY_LLM_TURN_RETRIES overrides it; 0 disables the retry.
const DefaultTurnRetries = 1

// truncatedTurnPromptRel is the continuation reminder, relative to PROJECT_ROOT.
const truncatedTurnPromptRel = "src/agent_policy/TURN_TRUNCATED.md"

// turnTruncationMarkers are what a truncated turn looks like in the error text of
// a run. They are matched case-insensitively, on the message rather than on a
// provider error code, because the SDK raises this as a plain run failure: the
// text is the only thing that says the turn was cut off rather than the session
// broken (the Cline SDK's wording is the first entry; a backend whose wording
// differs adds its own here).
var turnTruncationMarkers = []string{
	"maximum output token limit",
	"max output token limit",
	"maximum output tokens",
}

// isTruncatedTurn reports whether a failed run failed because a turn was cut off
// by the model's output limit.
func isTruncatedTurn(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range turnTruncationMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// turnRetryBudget is how many extra turns a truncated worker gets:
// AUTONOMY_LLM_TURN_RETRIES when it parses as a non-negative count, the default
// otherwise.
func turnRetryBudget() int {
	raw := strings.TrimSpace(os.Getenv("AUTONOMY_LLM_TURN_RETRIES"))
	if raw == "" {
		return DefaultTurnRetries
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return DefaultTurnRetries
	}
	return n
}

// turnTruncatedPrompt renders the continuation reminder for a worker whose turn
// was cut off. The frame is the host's (broker.WorkerFrame), so the reminder
// speaks about this worker — its identity, its workspace — the same way its task
// prompt did.
//
// It returns an error when the policy file cannot be read: a retry has to say
// what happened, so a missing reminder means no retry (the caller keeps the
// original failure instead of sending an aimless prompt).
func turnTruncatedPrompt(frame map[string]string) (string, error) {
	root, err := projectRoot()
	if err != nil {
		return "", fmt.Errorf("turn retry: %w", err)
	}
	path := filepath.Join(root, filepath.FromSlash(truncatedTurnPromptRel))
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("turn retry: read %s: %w", path, err)
	}
	return broker.RenderWorkerPrompt(string(b), nil, frame), nil
}
