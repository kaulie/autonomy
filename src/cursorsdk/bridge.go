package cursorsdk

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const readyPrefix = "cursor-sdk-bridge ready "

// BridgeEndpoint is where a running bridge listens and how to authenticate.
type BridgeEndpoint struct {
	URL           string
	AuthToken     string
	ServerVersion string
	PID           int
}

type bridgeReady struct {
	SchemaVersion int    `json:"schemaVersion"`
	ServerVersion string `json:"serverVersion"`
	PID           int    `json:"pid"`
	Transport     string `json:"transport"`
	Protocol      string `json:"protocol"`
	URL           string `json:"url"`
	AuthTokenFile string `json:"authTokenFile"`
	AuthToken     string `json:"authToken"` // older bridges only
}

// BridgeManager owns one cursor-sdk-bridge child process.
type BridgeManager struct {
	Binary    string
	Workspace string
	APIKey    string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stderr  io.ReadCloser
	started bool
}

func defaultBridgeBinary() string {
	if p := strings.TrimSpace(os.Getenv("CURSOR_SDK_BRIDGE_BIN")); p != "" {
		return p
	}
	name := "cursor-sdk-bridge"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidates := []string{
		filepath.Join("third_party", "bin", name),
		filepath.Join("cursor-sdk-bridge", "bin", name),
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "third_party", "bin", name),
			filepath.Join(wd, "..", "third_party", "bin", name),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return filepath.Join("third_party", "bin", name)
}

// Start spawns the bridge and blocks until the ready-line handshake completes.
func (m *BridgeManager) Start() (*BridgeEndpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil, &BridgeProcessError{Msg: "bridge already started"}
	}

	bin := m.Binary
	if bin == "" {
		bin = defaultBridgeBinary()
	}
	workspace := m.Workspace
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	workspace, _ = filepath.Abs(workspace)

	cmd := exec.Command(bin, "--workspace", workspace)
	env := append(os.Environ(), "CURSOR_SDK_CLIENT_LANGUAGE=go")
	if m.APIKey != "" {
		env = append(env, "CURSOR_API_KEY="+m.APIKey)
	}
	cmd.Env = env
	cmd.Stdout = io.Discard
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, &BridgeProcessError{Msg: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return nil, &BridgeProcessError{Msg: fmt.Sprintf("could not launch bridge %q: %v (run scripts/fetch-bridge.sh or set CURSOR_SDK_BRIDGE_BIN)", bin, err)}
	}
	m.cmd = cmd
	m.stderr = stderr

	ready, err := waitReadyLine(stderr, 30*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		m.cmd = nil
		return nil, &BridgeProcessError{Msg: err.Error()}
	}
	if ready.SchemaVersion != 1 || ready.Transport != "tcp" || ready.Protocol != "connect" {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		m.cmd = nil
		return nil, &BridgeProcessError{Msg: fmt.Sprintf("unsupported bridge discovery: schema=%v transport=%v protocol=%v", ready.SchemaVersion, ready.Transport, ready.Protocol)}
	}

	token := strings.TrimSpace(ready.AuthToken)
	if token == "" {
		b, err := os.ReadFile(ready.AuthTokenFile)
		if err != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			m.cmd = nil
			return nil, &BridgeProcessError{Msg: fmt.Sprintf("read auth token: %v", err)}
		}
		token = strings.TrimSpace(string(b))
	}

	// Keep draining stderr forever so a full pipe cannot block the bridge.
	go io.Copy(io.Discard, stderr)

	m.started = true
	return &BridgeEndpoint{
		URL:           strings.TrimRight(ready.URL, "/"),
		AuthToken:     token,
		ServerVersion: ready.ServerVersion,
		PID:           ready.PID,
	}, nil
}

// Stop shuts the bridge down (SIGTERM, then kill).
func (m *BridgeManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd == nil || m.cmd.Process == nil {
		return
	}
	_ = m.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_ = m.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = m.cmd.Process.Kill()
		<-done
	}
	m.cmd = nil
	m.started = false
}

func waitReadyLine(r io.Reader, timeout time.Duration) (*bridgeReady, error) {
	type result struct {
		ready *bridgeReady
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(r)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, readyPrefix) {
				continue
			}
			var ready bridgeReady
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, readyPrefix)), &ready); err != nil {
				ch <- result{err: fmt.Errorf("parse ready line: %w", err)}
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
