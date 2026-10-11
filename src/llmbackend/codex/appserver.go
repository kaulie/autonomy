package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// listLiveTimeout bounds a catalogue read: model/list is a handshake plus one
// or two RPCs, but a stuck CLI must not hold the create-task picker open.
const listLiveTimeout = 20 * time.Second

// listLiveCodexModels is the account-aware catalogue: spawn `codex app-server`,
// handshake, then paginate `model/list`. That is what the signed-in ChatGPT
// (or API-key) account can actually run — not a snapshot we typed last week.
func listLiveCodexModels(ctx context.Context, creds llmbackend.Creds) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bin := binary()
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("codex CLI: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, listLiveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "app-server")
	cmd.Env = environment(creds)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	session := &appServerSession{
		enc: json.NewEncoder(stdin),
		sc:  newLineScanner(stdout),
	}
	if _, err := session.call(1, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "autonomy",
			"title":   "autonomy",
			"version": "1",
		},
	}); err != nil {
		return nil, err
	}
	if err := session.notify("initialized", map[string]any{}); err != nil {
		return nil, err
	}

	out := make([]string, 0)
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := session.call(2+page, "model/list", params)
		if err != nil {
			return nil, err
		}
		var listed modelListResult
		if err := json.Unmarshal(raw, &listed); err != nil {
			return nil, fmt.Errorf("codex model/list: %w", err)
		}
		for _, model := range listed.Data {
			if model.Hidden {
				continue
			}
			id := strings.TrimSpace(model.ID)
			if id == "" {
				id = strings.TrimSpace(model.Model)
			}
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
		cursor = strings.TrimSpace(listed.NextCursor)
		if cursor == "" {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("codex model/list: empty catalogue")
	}
	return out, nil
}

func binary() string {
	if named := strings.TrimSpace(os.Getenv("AUTONOMY_CODEX_BIN")); named != "" {
		return named
	}
	// The release pack unpacks the bridge next to its node_modules; the SDK's
	// bundled `codex` lives there when the global CLI is not on PATH.
	if script := strings.TrimSpace(os.Getenv("AUTONOMY_CODEX_BRIDGE_SCRIPT")); script != "" {
		candidate := filepath.Join(filepath.Dir(script), "node_modules", ".bin", "codex")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "codex"
}

// environment is the process the CLI sees: the account's key/base URL when the
// pool has them, otherwise whatever `codex auth` already saved.
func environment(creds llmbackend.Creds) []string {
	env := os.Environ()
	if strings.TrimSpace(creds.APIKey) == "" && strings.TrimSpace(creds.BaseURL) == "" {
		return env
	}
	filtered := make([]string, 0, len(env)+3)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if creds.APIKey != "" && (key == "CODEX_API_KEY" || key == "OPENAI_API_KEY" || key == "AUTONOMY_CODEX_API_KEY") {
			continue
		}
		if creds.BaseURL != "" && (key == "OPENAI_BASE_URL" || key == "AUTONOMY_CODEX_BASE_URL") {
			continue
		}
		filtered = append(filtered, entry)
	}
	if key := strings.TrimSpace(creds.APIKey); key != "" {
		filtered = append(filtered, "CODEX_API_KEY="+key, "OPENAI_API_KEY="+key)
	}
	if base := strings.TrimSpace(creds.BaseURL); base != "" {
		filtered = append(filtered, "OPENAI_BASE_URL="+base)
	}
	return filtered
}

type appServerSession struct {
	enc *json.Encoder
	sc  *bufio.Scanner
}

func (s *appServerSession) notify(method string, params any) error {
	return s.enc.Encode(map[string]any{"method": method, "params": params})
}

func (s *appServerSession) call(id int, method string, params any) (json.RawMessage, error) {
	msg := map[string]any{"method": method, "id": id}
	if params != nil {
		msg["params"] = params
	}
	if err := s.enc.Encode(msg); err != nil {
		return nil, fmt.Errorf("codex %s: %w", method, err)
	}
	for s.sc.Scan() {
		var resp struct {
			ID     any             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(s.sc.Bytes(), &resp); err != nil {
			continue
		}
		if resp.Method != "" && resp.ID == nil {
			continue
		}
		if rpcID(resp.ID) != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("codex %s: %s", method, resp.Error.Message)
		}
		if len(resp.Result) == 0 {
			return json.RawMessage("{}"), nil
		}
		return resp.Result, nil
	}
	if err := s.sc.Err(); err != nil {
		return nil, fmt.Errorf("codex %s: %w", method, err)
	}
	return nil, fmt.Errorf("codex %s: no response", method)
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type modelListResult struct {
	Data []struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Hidden bool   `json:"hidden"`
	} `json:"data"`
	NextCursor string `json:"nextCursor"`
}

func rpcID(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return -1
	}
}

func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return sc
}
