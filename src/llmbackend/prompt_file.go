package llmbackend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// ProbePromptRel is the one-line live probe a harness asks with. Named here so the
	// file and its readers cannot drift — package autonomy embeds the same file
	// (docs/prompt.md). An agent's first prompt is not a harness file: it is the
	// runtime frame (GiveFirstPrompt), the same for every backend.
	ProbePromptRel = "src/agent_policy/PROBE.md"
)

// PromptFile reads one prompt file — `src/agent_policy/<name>.md` — the way every prompt in
// this runtime is read: **from disk at runtime**, so a deployment can edit the wording, with
// the copy this build carries as the last resort (the same rule package autonomy follows in
// src/prompt.go, docs/prompt.md).
//
// It exists so a harness can read a prompt file (today: the live probe) without keeping
// prompt text in Go. An agent's initialization words are not read here — they go out as
// the session's first turn (docs/prompt.md).
//
// The build's own copies are embedded in package autonomy (it is the package that owns
// src/agent_policy), so it registers them here at init — see SetPromptFallback.
func PromptFile(rel string) (string, error) {
	if root, err := ProjectRoot(); err == nil && strings.TrimSpace(root) != "" {
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			if text := strings.TrimSpace(string(b)); text != "" {
				return text, nil
			}
		}
	}
	if text, ok := promptFallback(rel); ok {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("prompt file %s is neither under PROJECT_ROOT nor carried by this build", rel)
}

var (
	promptFallbackMu sync.RWMutex
	promptFallbackFn func(string) (string, bool)
)

// SetPromptFallback registers the prompt copies this build carries, keyed by the same
// relative path PromptFile is asked for. Package autonomy calls it once at init; a build
// without that registration still reads from disk (PROJECT_ROOT), it just has no fallback.
func SetPromptFallback(fn func(rel string) (string, bool)) {
	promptFallbackMu.Lock()
	defer promptFallbackMu.Unlock()
	promptFallbackFn = fn
}

func promptFallback(rel string) (string, bool) {
	promptFallbackMu.RLock()
	defer promptFallbackMu.RUnlock()
	if promptFallbackFn == nil {
		return "", false
	}
	return promptFallbackFn(rel)
}
