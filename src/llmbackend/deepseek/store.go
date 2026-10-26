package deepseek

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DeepSeek's API is stateless: the conversation is whatever the caller sends. So this
// harness keeps it — one JSON file per session — and that file is what a resume reads back
// (the agent row records the session id, as it does for every harness).

// transcript is one session as it is stored.
type transcript struct {
	ID        string    `json:"id"`
	Model     string    `json:"model,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	Messages  []message `json:"messages"`
	UpdatedAt time.Time `json:"updated_at"`
}

// sessionsDir is where transcripts live: AUTONOMY_DEEPSEEK_SESSIONS_DIR, else
// ~/.autonomy/deepseek/sessions — outside a deployed runtime's directory, so a new
// release does not take the conversations with it.
func sessionsDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("AUTONOMY_DEEPSEEK_SESSIONS_DIR")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("deepseek sessions dir: %w", err)
	}
	return filepath.Join(home, ".autonomy", "deepseek", "sessions"), nil
}

// newSessionID mints a session id ("ds-" + 16 hex digits).
func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ds-" + hex.EncodeToString(b[:])
}

// transcriptPath is a session's file; an id that is not one of ours has none.
func transcriptPath(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("deepseek: invalid session id %q", id)
	}
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

// loadTranscript reads a stored session; ok=false when there is none (a fresh start).
func loadTranscript(id string) (transcript, bool, error) {
	path, err := transcriptPath(id)
	if err != nil {
		return transcript{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return transcript{}, false, nil
	}
	if err != nil {
		return transcript{}, false, err
	}
	var t transcript
	if err := json.Unmarshal(raw, &t); err != nil {
		return transcript{}, false, fmt.Errorf("deepseek: session %s: %w", id, err)
	}
	return t, true, nil
}

// saveTranscript writes a session atomically (temp file + rename).
func saveTranscript(t transcript) error {
	path, err := transcriptPath(t.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	t.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// deleteTranscript removes a one-shot session's file.
func deleteTranscript(id string) {
	if path, err := transcriptPath(id); err == nil {
		_ = os.Remove(path)
	}
}
