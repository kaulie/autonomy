package autonomy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const readyPrefix = "cursor-sdk-bridge ready "

// bridgeHTTP talks to the local bridge only — never via HTTP(S)_PROXY.
var bridgeHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy: nil,
	},
}

// CursorBridge is a thin Go adapter around the official cursor-sdk-bridge process.
// See https://cursor.com/docs/sdk/bridge
type CursorBridge struct {
	cmd    *exec.Cmd
	stderr io.ReadCloser
	base   string
	token  string
	apiKey string
}

type bridgeReady struct {
	SchemaVersion int    `json:"schemaVersion"`
	Transport     string `json:"transport"`
	Protocol      string `json:"protocol"`
	URL           string `json:"url"`
	AuthTokenFile string `json:"authTokenFile"`
}

// StartCursorBridge spawns cursor-sdk-bridge and completes the ready-line handshake.
// Binary resolution: CURSOR_SDK_BRIDGE_BIN, then <repo>/third_party/bin/cursor-sdk-bridge.
func StartCursorBridge(workspace string) (*CursorBridge, error) {
	apiKey := strings.TrimSpace(os.Getenv("CURSOR_API_KEY"))
	if apiKey == "" {
		return nil, fmt.Errorf("CURSOR_API_KEY is required")
	}
	bin, err := resolveBridgeBin()
	if err != nil {
		return nil, err
	}
	if workspace == "" {
		workspace, _ = os.Getwd()
	}

	cmd := exec.Command(bin, "--workspace", workspace)
	cmd.Env = append(os.Environ(),
		"CURSOR_API_KEY="+apiKey,
		"CURSOR_SDK_CLIENT_LANGUAGE=go",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bridge: %w", err)
	}

	ready, err := waitReady(stderr, 30*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	tokenBytes, err := os.ReadFile(ready.AuthTokenFile)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("read auth token: %w", err)
	}

	b := &CursorBridge{
		cmd:    cmd,
		stderr: stderr,
		base:   strings.TrimRight(ready.URL, "/"),
		token:  strings.TrimSpace(string(tokenBytes)),
		apiKey: apiKey,
	}
	// Keep draining stderr so a full pipe cannot block the bridge.
	go io.Copy(io.Discard, stderr)

	if err := b.ping(); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func resolveBridgeBin() (string, error) {
	if p := strings.TrimSpace(os.Getenv("CURSOR_SDK_BRIDGE_BIN")); p != "" {
		return p, nil
	}
	candidates := []string{
		filepath.Join("third_party", "bin", "cursor-sdk-bridge"),
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "third_party", "bin", "cursor-sdk-bridge"),
			filepath.Join(wd, "..", "third_party", "bin", "cursor-sdk-bridge"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs, nil
		}
	}
	return "", fmt.Errorf("cursor-sdk-bridge not found; set CURSOR_SDK_BRIDGE_BIN or run scripts/fetch-bridge.sh")
}

func waitReady(r io.Reader, timeout time.Duration) (*bridgeReady, error) {
	type result struct {
		ready *bridgeReady
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(r)
		// Ready JSON can be long.
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, readyPrefix) {
				continue
			}
			payload := strings.TrimPrefix(line, readyPrefix)
			var ready bridgeReady
			if err := json.Unmarshal([]byte(payload), &ready); err != nil {
				ch <- result{err: fmt.Errorf("parse ready line: %w", err)}
				return
			}
			if ready.SchemaVersion != 1 || ready.Transport != "tcp" || ready.Protocol != "connect" {
				ch <- result{err: fmt.Errorf("unsupported ready payload: %+v", ready)}
				return
			}
			ch <- result{ready: &ready}
			return
		}
		if err := sc.Err(); err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{err: fmt.Errorf("bridge exited before ready line")}
	}()

	select {
	case res := <-ch:
		return res.ready, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("bridge ready timeout after %s", timeout)
	}
}

func (b *CursorBridge) Close() error {
	if b == nil || b.cmd == nil || b.cmd.Process == nil {
		return nil
	}
	_, _ = b.postJSON("/sdk.v1.SdkBridgeControlService/Shutdown", map[string]any{})
	done := make(chan error, 1)
	go func() { done <- b.cmd.Wait() }()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		_ = b.cmd.Process.Kill()
		<-done
		return nil
	}
}

func (b *CursorBridge) ping() error {
	_, err := b.postJSON("/sdk.v1.SdkBridgeControlService/Ping", map[string]any{})
	return err
}

// CreateLocalAgent creates a local agent bound to cwd.
func (b *CursorBridge) CreateLocalAgent(modelID, cwd string) (string, error) {
	if modelID == "" {
		modelID = "composer-2"
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	body := map[string]any{
		"options": map[string]any{
			"model":  map[string]any{"id": modelID},
			"apiKey": b.apiKey,
			"local":  map[string]any{"cwd": []string{cwd}},
		},
	}
	raw, err := b.postJSON("/sdk.v1.SdkAgentService/CreateAgent", body)
	if err != nil {
		return "", err
	}
	var resp struct {
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", err
	}
	if resp.AgentID == "" {
		return "", fmt.Errorf("CreateAgent returned empty agentId: %s", string(raw))
	}
	return resp.AgentID, nil
}

// Prompt sends one user message and returns concatenated assistant text.
func (b *CursorBridge) Prompt(agentID, text string) (string, error) {
	body := map[string]any{
		"agentId": agentID,
		"message": map[string]any{"text": text},
		"options": map[string]any{
			// Prefer ask so decision turns do not enter local tool loops.
			"mode": "ask",
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	var frame bytes.Buffer
	frame.WriteByte(0)
	_ = binary.Write(&frame, binary.BigEndian, uint32(len(payload)))
	frame.Write(payload)

	req, err := http.NewRequest(http.MethodPost, b.base+"/sdk.v1.SdkAgentService/Send", &frame)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")

	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bmsg, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Send HTTP %d: %s", resp.StatusCode, string(bmsg))
	}

	var assistant strings.Builder
	var finalText string
	for {
		var flags byte
		var length uint32
		if err := binary.Read(resp.Body, binary.BigEndian, &flags); err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		if err := binary.Read(resp.Body, binary.BigEndian, &length); err != nil {
			return "", err
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(resp.Body, payload); err != nil {
			return "", err
		}
		if flags&0x02 != 0 {
			var end struct {
				Error json.RawMessage `json:"error"`
			}
			_ = json.Unmarshal(payload, &end)
			if len(end.Error) > 0 && string(end.Error) != "null" {
				return "", fmt.Errorf("Send stream error: %s", string(end.Error))
			}
			break
		}
		if len(payload) == 0 {
			continue // keepalive
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if rawResult, ok := env["result"]; ok {
			if t := extractResultText(rawResult); t != "" {
				finalText = t
			}
			continue
		}
		rawMsg, ok := env["sdkMessage"]
		if !ok {
			continue
		}
		if t := extractAssistantText(rawMsg); t != "" {
			assistant.WriteString(t)
		}
	}
	out := strings.TrimSpace(finalText)
	if out == "" {
		out = strings.TrimSpace(assistant.String())
	}
	if out == "" {
		return "", fmt.Errorf("no assistant text in run stream")
	}
	return out, nil
}

func extractResultText(raw json.RawMessage) string {
	var result struct {
		Result struct {
			Text string `json:"text"`
		} `json:"result"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return ""
	}
	if result.Result.Text != "" {
		return result.Result.Text
	}
	return result.Text
}

func extractAssistantText(raw json.RawMessage) string {
	var msg struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Message struct {
			Text    string `json:"text"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &msg) != nil {
		return ""
	}
	if msg.Type != "" && msg.Type != "assistant" {
		return ""
	}
	if msg.Text != "" {
		return msg.Text
	}
	if msg.Message.Text != "" {
		return msg.Message.Text
	}
	var b strings.Builder
	for _, c := range msg.Message.Content {
		if c.Type == "text" || c.Type == "" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func (b *CursorBridge) postJSON(path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, b.base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d: %s", path, resp.StatusCode, string(raw))
	}
	return raw, nil
}
