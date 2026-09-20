package cursorsdk

import (
	"strings"
	"testing"
)

// TestBridgeEnvDropsTheProxiesItInherited: the bridge must not be handed the proxy
// the runtime itself runs behind. That proxy is for the runtime's own egress; a
// bridge that inherits one it was never configured for dials it and then waits
// forever — CreateAgent hangs with no error, three minutes / a call timeout at a
// time, which is exactly how a deployment whose platform injects HTTP(S)_PROXY
// wedges every instruction it receives.
func TestBridgeEnvDropsTheProxiesItInherited(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"HTTP_PROXY=http://127.0.0.1:7897",
		"http_proxy=http://127.0.0.1:7897",
		"HTTPS_PROXY=http://127.0.0.1:7897",
		"https_proxy=http://127.0.0.1:7897",
		"ALL_PROXY=http://127.0.0.1:7897",
		"all_proxy=http://127.0.0.1:7897",
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
		"CURSOR_API_KEY=crsr_test",
	}
	env := bridgeEnv(environ, "")
	for _, kv := range env {
		name := strings.SplitN(kv, "=", 2)[0]
		switch strings.ToLower(name) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			t.Fatalf("bridge child was given %q: its egress is the Cursor API, not this process's errands", kv)
		}
	}
	for _, want := range []string{"PATH=/usr/bin", "CURSOR_API_KEY=crsr_test", "CURSOR_SDK_CLIENT_LANGUAGE=go"} {
		if !contains(env, want) {
			t.Fatalf("bridge env %v is missing %q", env, want)
		}
	}
}

// ... and a deployment whose Cursor egress really does need one says so for the
// bridge alone (CURSOR_SDK_BRIDGE_PROXY).
func TestBridgeEnvTakesTheBridgesOwnProxy(t *testing.T) {
	const own = "http://127.0.0.1:1080"
	env := bridgeEnv([]string{"HTTPS_PROXY=http://127.0.0.1:7897"}, own)
	for _, want := range []string{"HTTPS_PROXY=" + own, "HTTP_PROXY=" + own, "ALL_PROXY=" + own} {
		if !contains(env, want) {
			t.Fatalf("bridge env %v is missing %q", env, want)
		}
	}
	for _, kv := range env {
		if strings.Contains(kv, "7897") {
			t.Fatalf("bridge env %v still carries the runtime's own proxy", env)
		}
	}
}

func TestBridgeEnvReadsTheEnvKnob(t *testing.T) {
	t.Setenv(BridgeProxyEnv, " http://127.0.0.1:1080 ")
	if got := bridgeProxy(); got != "http://127.0.0.1:1080" {
		t.Fatalf("CURSOR_SDK_BRIDGE_PROXY → %q, want the trimmed url", got)
	}
	t.Setenv(BridgeProxyEnv, "")
	if got := bridgeProxy(); got != "" {
		t.Fatalf("an unset CURSOR_SDK_BRIDGE_PROXY → %q, want no proxy", got)
	}
}

func contains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
