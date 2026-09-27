package start_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The listen-address helpers are what a remote-server deploy actually changes:
// host comes from AUTONOMY_HTTP_HOST / SERVICE_HOST, port stays the contract
// port, and a wildcard bind is probed on loopback. start.sh sources the same
// file the binary below execs, so a mismatch here is a mismatch on the box.

func helper() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller")
	}
	// src/tests/start → repo root → scripts/listen_addr.sh
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", "listen_addr.sh")
}

func runHelper(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{helper()}, args...)...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestComposeHTTPAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host, port, want string
		fail             bool
	}{
		{"127.0.0.1", "4300", "127.0.0.1:4300", false},
		{"0.0.0.0", "4300", "0.0.0.0:4300", false},
		{"::", "4300", "[::]:4300", false},
		{"[::1]", "4300", "[::1]:4300", false},
		{"10.0.0.9", "8080", "10.0.0.9:8080", false},
		{"", "4300", "", true},
		{"127.0.0.1", "0", "", true},
		{"127.0.0.1", "65536", "", true},
		{"127.0.0.1", "not-a-port", "", true},
		{"127.0.0.1 extra", "4300", "", true},
	}
	for _, tc := range cases {
		got, err := runHelper(t, nil, "compose", tc.host, tc.port)
		if tc.fail {
			if err == nil {
				t.Fatalf("compose %q %q succeeded with %q, want failure", tc.host, tc.port, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("compose %q %q: %v (%s)", tc.host, tc.port, err, got)
		}
		if got != tc.want {
			t.Fatalf("compose %q %q = %q want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

func TestResolveListenHostPriority(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"default loopback", nil, "127.0.0.1"},
		{"platform host", []string{"SERVICE_HOST=10.0.0.9"}, "10.0.0.9"},
		{"env wins over platform", []string{"AUTONOMY_HTTP_HOST=0.0.0.0", "SERVICE_HOST=10.0.0.9"}, "0.0.0.0"},
		{"env only", []string{"AUTONOMY_HTTP_HOST=192.168.1.8"}, "192.168.1.8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runHelper(t, tc.env, "host")
			if err != nil {
				t.Fatalf("%v (%s)", err, got)
			}
			if got != tc.want {
				t.Fatalf("host = %q want %q", got, tc.want)
			}
		})
	}
}

func TestHealthProbeRewritesWildcard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host, want string
	}{
		{"0.0.0.0", "127.0.0.1"},
		{"::", "127.0.0.1"},
		{"[::]", "127.0.0.1"},
		{"127.0.0.1", "127.0.0.1"},
		{"10.0.0.9", "10.0.0.9"},
		{"::1", "[::1]"},
	}
	for _, tc := range cases {
		got, err := runHelper(t, nil, "probe", tc.host)
		if err != nil {
			t.Fatalf("probe %q: %v (%s)", tc.host, err, got)
		}
		if got != tc.want {
			t.Fatalf("probe %q = %q want %q", tc.host, got, tc.want)
		}
	}
}

func TestStartScriptSourcesListenAddr(t *testing.T) {
	start := filepath.Join(filepath.Dir(helper()), "start.sh")
	body, err := os.ReadFile(start)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, need := range []string{
		`listen_addr.sh`,
		`resolve_listen_host`,
		`compose_http_addr`,
		`health_probe_host`,
	} {
		if !strings.Contains(text, need) {
			t.Errorf("scripts/start.sh does not use %s (remote bind would stay on 127.0.0.1)", need)
		}
	}
	if strings.Contains(text, `AUTONOMY_HTTP_ADDR="127.0.0.1:${PORT}"`) {
		t.Error("scripts/start.sh still hard-codes 127.0.0.1; a remote server cannot accept off-box traffic")
	}
}

func TestReleasePackageCarriesListenAddrAndUnit(t *testing.T) {
	root := filepath.Join(filepath.Dir(helper()), "..")
	build, err := os.ReadFile(filepath.Join(root, "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(build)
	for _, need := range []string{"listen_addr.sh", "autonomyd.service"} {
		if !strings.Contains(text, need) {
			t.Errorf("build.sh does not copy %s into the release package", need)
		}
	}
}
