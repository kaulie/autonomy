// Package clinesdk is the Cline harness's own layer: it says *where the Cline bridge
// lives* and *what environment points at it*, then hands everything else to the shared
// protocol machinery in src/bridgesdk (spawn/handshake, NDJSON channel, run streaming).
//
// Everything exported here is either the Cline-specific config or an alias of the shared
// client, so callers keep writing clinesdk.Client / clinesdk.RunEvent while the code that
// drives a bridge exists exactly once.
package clinesdk

import (
	"path/filepath"

	"github.com/kaulie/autonomy/src/bridgesdk"
)

// Protocol is the bridge protocol version this client speaks.
const Protocol = "cline-bridge/1"

// Config describes the Cline bridge to the shared machinery.
func Config() bridgesdk.Config {
	return bridgesdk.Config{
		Name:          "cline",
		Protocol:      Protocol,
		NodeBinEnv:    "AUTONOMY_CLINE_NODE_BIN",
		ScriptEnv:     "AUTONOMY_CLINE_BRIDGE_SCRIPT",
		ScriptRel:     filepath.Join("src", "clinesdk", "bridge", "bridge.mjs"),
		InstallHint:   "the release package ships the bridge with its dependencies; a dev checkout needs scripts/install-cline-bridge.sh",
		ProviderIDEnv: "AUTONOMY_CLINE_PROVIDER",
		ModelIDEnv:    "AUTONOMY_CLINE_MODEL",
		APIKeyEnv:     "AUTONOMY_CLINE_API_KEY",
		BaseURLEnv:    "AUTONOMY_CLINE_BASE_URL",
		DefaultMode:   DefaultMode,
	}
}

// NewClient builds a Cline client (shared implementation, Cline configuration).
func NewClient(opts ...ClientOption) *Client { return bridgesdk.NewClient(Config(), opts...) }

// NewBridgeManager builds the process manager for the Cline bridge.
func NewBridgeManager() *BridgeManager { return &BridgeManager{Config: Config()} }

// The shared protocol client, under the names this harness's callers already use.
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

// DefaultMode is the Cline mode used when the caller names none.
const DefaultMode = bridgesdk.DefaultMode

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
