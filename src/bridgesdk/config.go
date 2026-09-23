package bridgesdk

import (
	"os"
	"path/filepath"
	"strings"
)

// Config is what makes a bridge client concrete: it names the protocol the ready line must
// carry, where the bridge script lives, what the caller may set in the environment, and how
// the harness labels itself in logs and errors.
//
// Everything else in this package — spawn/handshake, the NDJSON channel, request/response
// correlation, run streaming, usage decoding — is protocol machinery shared by every
// harness, which is the point: adding a backend (codex, …) is a Config plus its bridge
// script and event mapping, not another copy of this code.
type Config struct {
	// Name labels this bridge in logs and error text, e.g. "cline".
	Name string
	// Protocol is the protocol string the ready line must carry, e.g. "cline-bridge/1".
	// Empty accepts whatever the bridge announces.
	Protocol string
	// NodeBinEnv / ScriptEnv are the environment variables that override the node binary
	// and the bridge script (how a deployment points at its own bridge).
	NodeBinEnv string
	ScriptEnv  string
	// ScriptRel is the repo-relative default script path, searched from the process's
	// working directory upwards; e.g. src/clinesdk/bridge/bridge.mjs.
	ScriptRel string
	// InstallHint tells a developer how to install the bridge's dependencies (the release
	// package ships them; a fresh checkout does not).
	InstallHint string
	// ProviderModelEnvs are the environment variables that supply the provider defaults.
	// Each may be empty: a harness that needs none simply leaves them unset.
	ProviderIDEnv string
	ModelIDEnv    string
	APIKeyEnv     string
	BaseURLEnv    string
	// DefaultMode is the mode applied when the caller names none.
	DefaultMode string
}

// normalize fills the generic defaults, so a Config only has to say what is specific to it.
func (c Config) normalize() Config {
	if c.Name == "" {
		c.Name = "llm"
	}
	if c.NodeBinEnv == "" {
		c.NodeBinEnv = "AUTONOMY_" + strings.ToUpper(c.Name) + "_NODE_BIN"
	}
	if c.InstallHint == "" {
		c.InstallHint = "install its dependencies in dev (see scripts/)"
	}
	return c
}

func (c Config) label() string { return c.normalize().Name + " bridge" }

// env reads one of the configured environment variables, trimmed.
func (c Config) env(name string) string {
	if name == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(name))
}

// nodeBin is the node executable to run the bridge with.
func (c Config) nodeBin() string {
	if v := c.env(c.normalize().NodeBinEnv); v != "" {
		return v
	}
	return "node"
}

// script is the path to the bridge script: the configured environment variable wins, else
// the repo-relative default looked up from the working directory upwards.
func (c Config) script() string {
	c = c.normalize()
	if v := c.env(c.ScriptEnv); v != "" {
		return v
	}
	if c.ScriptRel == "" {
		return ""
	}
	candidates := []string{c.ScriptRel}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, c.ScriptRel),
			filepath.Join(wd, "..", c.ScriptRel),
			filepath.Join(wd, "..", "..", c.ScriptRel),
		)
	}
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}
	return c.ScriptRel
}
