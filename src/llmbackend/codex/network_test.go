package codex_test

import (
	"testing"

	"github.com/kaulie/autonomy/src/codexsdk"
	"github.com/kaulie/autonomy/src/llmbackend"
	"github.com/kaulie/autonomy/src/llmbackend/codex"
)

// Whether a sandboxed turn may reach the network is the deployment's call, and it is a variable:
// per mode (a planner's read-only cycle vs a worker's), switched by AUTONOMY_CODEX_NETWORK.
func TestTheNetworkSwitchIsPerModeAndExternal(t *testing.T) {
	planner := codex.CodexModeFor(llmbackend.ModePlan)
	worker := codexsdk.DefaultMode
	cases := []struct {
		name      string
		env       string
		plannerOK bool
		workerOK  bool
	}{
		{name: "nothing set: both (what a task needs to fetch its repository)", env: "", plannerOK: true, workerOK: true},
		{name: "all", env: "all", plannerOK: true, workerOK: true},
		{name: "off", env: "off", plannerOK: false, workerOK: false},
		{name: "0", env: "0", plannerOK: false, workerOK: false},
		{name: "false", env: "false", plannerOK: false, workerOK: false},
		{name: "no", env: "no", plannerOK: false, workerOK: false},
		{name: "none", env: "none", plannerOK: false, workerOK: false},
		{name: "worker only", env: "worker", plannerOK: false, workerOK: true},
		{name: "planner only", env: "planner", plannerOK: true, workerOK: false},
		{name: "case and spaces do not matter", env: "  Planner ", plannerOK: true, workerOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(codex.EnvNetwork, tc.env)
			if got := codex.NetworkFor(planner); got != tc.plannerOK {
				t.Fatalf("env=%q planner mode=%q → %v, want %v", tc.env, planner, got, tc.plannerOK)
			}
			if got := codex.NetworkFor(worker); got != tc.workerOK {
				t.Fatalf("env=%q worker mode=%q → %v, want %v", tc.env, worker, got, tc.workerOK)
			}
		})
	}
}
