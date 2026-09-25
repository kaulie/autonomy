package codex

import (
	"os"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// Whether a Codex turn may reach the network *from inside its sandbox* is a deployment
// decision, and this file is where it is made.
//
// The sandbox is what makes codex safe to run unattended; the CLI's own default is that it
// cannot reach the network at all. That default is also why a sandboxed turn could not do the
// work it was asked for: the host's egress here is a **localhost proxy** the sandbox cannot
// see, and without it a git remote does not even resolve — so a `code_edit` worker with an
// empty workspace had no way to fill it (2026-09-25, task-bbef8a1899294353: the planner
// correctly reported "worker 无法获取仓库源码", and the worker's own words were "克隆仓库失败：
// 配置的代理 127.0.0.1:7897 无法连接；绕过代理后无法解析 github.com").
//
// The switch is external — a deployment sets it, not the code:
//
//	AUTONOMY_CODEX_NETWORK=all       both modes (the default)
//	                    =worker      only the agent mode (workspace-write)
//	                    =planner     only the plan mode (read-only)
//	                    =off         neither ("0" / "false" / "no" / "none" also mean this)
//
// It is read per turn: change it and restart, nothing else.
const EnvNetwork = "AUTONOMY_CODEX_NETWORK"

// NetworkFor resolves the switch for one session mode, in the bridge's own words: `plan` is a
// planner's read-only cycle, `agent` everything else (see CodexModeFor).
func NetworkFor(mode string) bool {
	planner := strings.EqualFold(strings.TrimSpace(mode), CodexModeFor(llmbackend.ModePlan))
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvNetwork))) {
	case "0", "off", "false", "no", "none":
		return false
	case "worker", "workers", "agent", "agents":
		return !planner
	case "planner", "planners", "plan", "plans":
		return planner
	default:
		return true
	}
}
