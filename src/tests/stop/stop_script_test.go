package stop_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func scriptsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts")
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitListen(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s after %s", addr, timeout)
}

func buildDummy(t *testing.T, dest string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "dummy.go")
	body := `package main
import (
	"net"
	"os"
)
func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("SERVICE_PORT"))
	if err != nil {
		os.Exit(2)
	}
	defer ln.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}
}
`
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", dest, src)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build dummy autonomyd: %v\n%s", err, out)
	}
}

func runStop(t *testing.T, runtimeDir string, port int) string {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(scriptsDir(t), "stop.sh"))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"RUNTIME_DIR=" + runtimeDir,
		fmt.Sprintf("SERVICE_PORT=%d", port),
		"AUTONOMY_STOP_TIMEOUT=2",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("stop.sh: %v\n%s", err, out)
	}
	return string(out)
}

func TestStopScriptFindsOwnListenerWithoutPidFile(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof is required for the port fallback")
	}
	port := freePort(t)
	runtimeDir := t.TempDir()
	bin := filepath.Join(runtimeDir, "bin", "autonomyd")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runtimeDir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	buildDummy(t, bin)

	proc := exec.Command(bin)
	proc.Env = []string{fmt.Sprintf("SERVICE_PORT=%d", port)}
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Process.Kill() }()
	waitListen(t, port, 3*time.Second)

	out := runStop(t, runtimeDir, port)
	if !strings.Contains(out, "残留进程") && !strings.Contains(out, "TERM") {
		t.Fatalf("stop.sh did not mention leftover/TERM, output:\n%s", out)
	}

	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "signal") && proc.ProcessState != nil && !proc.ProcessState.Exited() {
			t.Fatalf("dummy still running after stop.sh: %v\n%s", err, out)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("dummy still running 4s after stop.sh\n%s", out)
	}

	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
	if err == nil {
		_ = c.Close()
		t.Fatalf("port %d still accepting after stop.sh\n%s", port, out)
	}
}

func TestStopScriptStopsPidFileProcess(t *testing.T) {
	port := freePort(t)
	runtimeDir := t.TempDir()
	bin := filepath.Join(runtimeDir, "bin", "autonomyd")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(runtimeDir, "backend")
	if err := os.MkdirAll(backend, 0o755); err != nil {
		t.Fatal(err)
	}
	buildDummy(t, bin)

	proc := exec.Command(bin)
	proc.Env = []string{fmt.Sprintf("SERVICE_PORT=%d", port)}
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Process.Kill() }()
	waitListen(t, port, 3*time.Second)
	if err := os.WriteFile(filepath.Join(backend, "runtime.pid"), []byte(fmt.Sprintf("%d\n", proc.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runStop(t, runtimeDir, port)
	if !strings.Contains(out, "TERM") {
		t.Fatalf("stop.sh did not TERM the pid-file process:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(backend, "runtime.pid")); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed, err=%v\n%s", err, out)
	}

	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatalf("pid-file process still running after stop.sh\n%s", out)
	}
}

func TestStopScriptLeavesForeignListenerAlone(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof is required for the port fallback")
	}
	port := freePort(t)
	runtimeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runtimeDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runtimeDir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	out := runStop(t, runtimeDir, port)
	if strings.Contains(out, "TERM") || strings.Contains(out, "残留进程") {
		t.Fatalf("stop.sh tried to stop a foreign listener:\n%s", out)
	}

	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("foreign listener should still be up: %v\n%s", err, out)
	}
	_ = c.Close()
}

func TestStopScriptMentionsPortFallback(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(scriptsDir(t), "stop.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, need := range []string{
		"our_listeners",
		"is_our_runtime_process",
		"is_running",
		"backend/runtime.pid",
		"lsof",
		"SERVICE_PORT",
	} {
		if !strings.Contains(text, need) {
			t.Errorf("scripts/stop.sh is missing %q", need)
		}
	}
	if strings.Contains(text, "没有 pid 文件，视为未运行") && !strings.Contains(text, "our_listeners") {
		t.Error("scripts/stop.sh still treats a missing pid file as not running")
	}
}
