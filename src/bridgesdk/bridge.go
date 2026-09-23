package bridgesdk

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

// BridgeManager owns one `node bridge.mjs` child process and speaks the NDJSON
// protocol with it over stdio.
type BridgeManager struct {
	// Config says which bridge this is (protocol, script, env names, label).
	Config  Config
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

// Start spawns the bridge (if needed) and blocks until the ready handshake.
func (m *BridgeManager) Start(ctx context.Context) (*BridgeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		info := m.ready
		return &info, nil
	}
	cfg := m.Config.normalize()
	nodeBin := m.NodeBin
	if nodeBin == "" {
		nodeBin = cfg.nodeBin()
	}
	script := m.Script
	if script == "" {
		script = cfg.script()
	}
	abs, err := filepath.Abs(script)
	if err != nil {
		abs = script
	}
	if len(m.Command) == 0 {
		if _, err := os.Stat(abs); err != nil {
			return nil, bridgeErr("%s script not found at %s (set %s; %s)", cfg.label(), abs, cfg.ScriptEnv, cfg.InstallHint)
		}
	}
	// The child's working directory is the script's own directory — unless the caller
	// brought its own command (a fake bridge, or an externally managed one), in which case
	// the script path says nothing about where it should run.
	dir := m.Dir
	if dir == "" && len(m.Command) == 0 {
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
		return nil, bridgeErr("could not launch %s %s: %v (%s)", nodeBin, abs, err, cfg.InstallHint)
	}
	// One scanner for the whole process lifetime: the ready line and every later
	// message must come from the same buffered reader.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	// Keep draining stderr forever so a full pipe cannot block the bridge. The
	// bridge already tags its own lines ("[<name>-bridge] info: …"), so only
	// untagged output (a Node warning, for example) gets the prefix added here —
	// otherwise every line reads "[<name>-bridge] [<name>-bridge] …".
	go func() {
		stderrScanner := bufio.NewScanner(stderr)
		stderrScanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for stderrScanner.Scan() {
			line := stderrScanner.Text()
			if strings.HasPrefix(line, "["+cfg.Name+"-bridge]") {
				fmt.Fprintln(os.Stderr, line)
				continue
			}
			fmt.Fprintf(os.Stderr, "[%s-bridge] %s\n", cfg.Name, line)
		}
	}()

	info, err := waitReadyLine(sc, nodeBin, cfg, 60*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	if cfg.Protocol != "" && info.Protocol != cfg.Protocol {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, bridgeErr("unsupported %s protocol %q (want %q)", cfg.label(), info.Protocol, cfg.Protocol)
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

func waitReadyLine(sc *bufio.Scanner, nodeBin string, cfg Config, timeout time.Duration) (*BridgeInfo, error) {
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
		ch <- result{err: bridgeErr("%s exited before the ready line (node missing? try `%s --version`)", cfg.label(), nodeBin)}
	}()
	select {
	case res := <-ch:
		return res.info, res.err
	case <-time.After(timeout):
		return nil, bridgeErr("%s ready timeout after %s", cfg.label(), timeout)
	}
}
