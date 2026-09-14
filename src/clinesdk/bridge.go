package clinesdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Protocol is the bridge protocol version this client speaks.
const Protocol = "cline-bridge/1"

// BridgeManager owns one `node bridge.mjs` child process and speaks the NDJSON
// protocol with it over stdio.
type BridgeManager struct {
	NodeBin string // node executable (default "node")
	Script  string // path to bridge.mjs
	Dir     string // working directory for the child (default: the script's dir)
	Env     []string
	// Command, when set, replaces the default `node <script>` invocation. Tests
	// use it to run a fake bridge (for example the test binary itself).
	Command []string

	mu        sync.Mutex
	cmd       *exec.Cmd
	transport *transport
	started   bool
	ready     BridgeInfo
}

// BridgeInfo is the handshake the bridge prints before serving requests.
type BridgeInfo struct {
	Protocol string `json:"protocol"`
	PID      int    `json:"pid"`
	Node     string `json:"node"`
	SDK      string `json:"sdk"`
}

func defaultNodeBin() string {
	if v := strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_NODE_BIN")); v != "" {
		return v
	}
	return "node"
}

func defaultBridgeScript() string {
	if p := strings.TrimSpace(os.Getenv("AUTONOMY_CLINE_BRIDGE_SCRIPT")); p != "" {
		return p
	}
	rel := filepath.Join("src", "clinesdk", "bridge", "bridge.mjs")
	candidates := []string{rel}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, rel),
			filepath.Join(wd, "..", rel),
			filepath.Join(wd, "..", "..", rel),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return rel
}

// Start spawns the bridge (if needed) and blocks until the ready handshake.
func (m *BridgeManager) Start(ctx context.Context) (*BridgeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		info := m.ready
		return &info, nil
	}
	nodeBin := m.NodeBin
	if nodeBin == "" {
		nodeBin = defaultNodeBin()
	}
	script := m.Script
	if script == "" {
		script = defaultBridgeScript()
	}
	abs, err := filepath.Abs(script)
	if err != nil {
		abs = script
	}
	if len(m.Command) == 0 {
		if _, err := os.Stat(abs); err != nil {
			return nil, bridgeErr("bridge script not found at %s (set AUTONOMY_CLINE_BRIDGE_SCRIPT; install deps with scripts/install-cline-bridge.sh)", abs)
		}
	}
	dir := m.Dir
	if dir == "" {
		dir = filepath.Dir(abs)
	}
	args := m.Command
	if len(args) == 0 {
		args = []string{nodeBin, abs}
	}
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // path comes from configuration
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), m.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, bridgeErr("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, bridgeErr("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, bridgeErr("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, bridgeErr("could not launch %s %s: %v (install deps with scripts/install-cline-bridge.sh)", nodeBin, abs, err)
	}
	// One scanner for the whole process lifetime: the ready line and every later
	// message must come from the same buffered reader.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	// Keep draining stderr forever so a full pipe cannot block the bridge.
	go func() {
		stderrScanner := bufio.NewScanner(stderr)
		stderrScanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for stderrScanner.Scan() {
			fmt.Fprintf(os.Stderr, "[cline-bridge] %s\n", stderrScanner.Text())
		}
	}()

	info, err := waitReadyLine(sc, 60*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	if info.Protocol != Protocol {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, bridgeErr("unsupported bridge protocol %q (want %q)", info.Protocol, Protocol)
	}

	m.cmd = cmd
	m.ready = *info
	m.started = true
	m.transport = newTransport(stdin, sc)
	m.transport.start()
	return &m.ready, nil
}

// Stop shuts the bridge down (shutdown command, then SIGTERM/kill).
func (m *BridgeManager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	tr := m.transport
	m.cmd = nil
	m.transport = nil
	m.started = false
	m.mu.Unlock()
	if tr != nil {
		tr.close()
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
}

func waitReadyLine(sc *bufio.Scanner, timeout time.Duration) (*BridgeInfo, error) {
	type result struct {
		info *BridgeInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] != '{' {
				continue
			}
			// Key order is the producer's business: match on the message type.
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &envelope); err != nil || envelope.Type != "ready" {
				continue
			}
			var info BridgeInfo
			if err := json.Unmarshal([]byte(line), &info); err != nil {
				ch <- result{err: bridgeErr("parse ready line: %v", err)}
				return
			}
			ch <- result{info: &info}
			return
		}
		if err := sc.Err(); err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{err: bridgeErr("bridge exited before the ready line (node missing? try `%s --version`)", defaultNodeBin())}
	}()
	select {
	case res := <-ch:
		return res.info, res.err
	case <-time.After(timeout):
		return nil, bridgeErr("bridge ready timeout after %s", timeout)
	}
}
