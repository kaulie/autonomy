package autonomy

import (
	"os"
	"strings"
)

// Which account a capability-acquired worker runs on — and with it which harness, credential,
// model and workspace root — is one decision, and it is not the capability's to hardcode: it is
// `broker.AcquireAgentOpts.ExtendsPlannerAgent` ("does this worker extend the agent that
// delegated to it?"), whose default is this deployment-level switch.
//
// The rule the default follows (2026-09-24): a task runs on one account, and a worker is part
// of that task, so a worker must not spend a second account's quota on the same task's work —
// a task on a codex account had its code written by a cline worker because workers took the
// process default backend and that harness's own default account. A deployment that wants the
// old behaviour (a worker picks its own provider) switches this off.
const EnvWorkerExtendsPlannerAgent = "AUTONOMY_WORKER_EXTENDS_PLANNER_AGENT"

// workerExtendsPlannerAgent resolves that decision for one acquisition: what the capability
// said (`AcquireAgentOpts.ExtendsPlannerAgent`), else the runtime's default. The default is on
// unless the environment switches it off the way AUTONOMY_CONTEXT_BUILDER does
// ("0" / "off" / "false" / "no").
func workerExtendsPlannerAgent(choice *bool) bool {
	if choice != nil {
		return *choice
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvWorkerExtendsPlannerAgent))) {
	case "0", "off", "false", "no":
		return false
	default:
		return true
	}
}
