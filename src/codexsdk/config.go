// Package codexsdk is the Codex harness's own layer: like clinesdk it says *where the Codex
// bridge lives* and *what environment points at it*, and leaves the protocol machinery to
// src/bridgesdk.
//
// Codex has no provider catalogue of its own — the pipeline is OpenAI's — so this config
// names only a model, a key and a base URL. Its reasoning modes are `plan` (read-only) and
// `agent` (the workspace may be written), which is why the default mode differs from
// Cline's.
package codexsdk

import (
	"path/filepath"

	"github.com/kaulie/autonomy/src/bridgesdk"
)

// Protocol is the bridge protocol version this client speaks.
const Protocol = "codex-bridge/1"

// DefaultMode runs a Codex thread that may change the workspace (the bridge maps it onto
// the `workspace-write` sandbox).
const DefaultMode = "agent"

// Config describes the Codex bridge to the shared machinery.
func Config() bridgesdk.Config {
	return bridgesdk.Config{
		Name:        "codex",
		Protocol:    Protocol,
		NodeBinEnv:  "AUTONOMY_CODEX_NODE_BIN",
		ScriptEnv:   "AUTONOMY_CODEX_BRIDGE_SCRIPT",
		ScriptRel:   filepath.Join("src", "codexsdk", "bridge", "bridge.mjs"),
		InstallHint: "the release package ships the bridge with its dependencies; a dev checkout needs scripts/install-codex-bridge.sh",
		ModelIDEnv:  "AUTONOMY_CODEX_MODEL",
		APIKeyEnv:   "AUTONOMY_CODEX_API_KEY",
		BaseURLEnv:  "AUTONOMY_CODEX_BASE_URL",
		DefaultMode: DefaultMode,
	}
}

// NewClient builds a Codex client (shared implementation, Codex configuration).
func NewClient(opts ...bridgesdk.ClientOption) *bridgesdk.Client {
	return bridgesdk.NewClient(Config(), opts...)
}

// NewBridgeManager builds the process manager for the Codex bridge.
func NewBridgeManager() *bridgesdk.BridgeManager { return &bridgesdk.BridgeManager{Config: Config()} }

// The shared protocol client, under the names a harness's callers expect.
type (
	Client             = bridgesdk.Client
	ClientOption       = bridgesdk.ClientOption
	Agent              = bridgesdk.Agent
	AgentFactory       = bridgesdk.AgentFactory
	CreateOptions      = bridgesdk.CreateOptions
	Run                = bridgesdk.Run
	RunEvent           = bridgesdk.RunEvent
	RunResult          = bridgesdk.RunResult
	RunUsage           = bridgesdk.RunUsage
	BridgeManager      = bridgesdk.BridgeManager
	BridgeInfo         = bridgesdk.BridgeInfo
	BridgeProcessError = bridgesdk.BridgeProcessError
	RPCError           = bridgesdk.RPCError
	RunError           = bridgesdk.RunError
	Model              = bridgesdk.Model
)

// Run/LLM statuses, re-exported so callers do not import two packages for one concept.
const (
	LLMStatusFinished  = bridgesdk.LLMStatusFinished
	LLMStatusError     = bridgesdk.LLMStatusError
	LLMStatusCancelled = bridgesdk.LLMStatusCancelled
)

// Client options.
var (
	WithProvider     = bridgesdk.WithProvider
	WithModel        = bridgesdk.WithModel
	WithAPIKey       = bridgesdk.WithAPIKey
	WithBaseURL      = bridgesdk.WithBaseURL
	WithMode         = bridgesdk.WithMode
	WithWorkspace    = bridgesdk.WithWorkspace
	WithSystemPrompt = bridgesdk.WithSystemPrompt
	WithManager      = bridgesdk.WithManager
)
