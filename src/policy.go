package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Policy is the runtime's own rules for the agents it runs, as data: the
// sentences rendered into {{CONSTRAINTS}}, next to the facts of the cycle (its
// task and sandbox).
//
// It is its own thing on purpose (see docs/policy.md): a rule is not a
// capability's property, so it is not written inside a capability, and it is not
// the prompt layer's wording either — "deploying is the Runtime's move" is a fact
// about deploying, and the code that renders prompts has no business knowing
// which domains exist.
//
// The wording lives in a file, for the same reason the agent policies do
// (src/agent_policy/AGENT_V2.md, CODE_EDIT.md, DEPLOYMENT_MONITOR.md): a rule is
// a sentence someone owns, and changing it must not need a rebuild.
const DefaultPolicyRel = "src/agent_policy/CONSTRAINTS.json"

// policyConstraints reads the runtime's policy for {{CONSTRAINTS}}: a flat JSON
// object of key → sentence, keyed the way the runtime's own facts are.
//
// A runtime without the file has no rules of its own to add, which is not an
// error. A file that is there but cannot be read is reported on stderr and adds
// nothing: a prompt still has to render, and a policy nobody can read has to be
// visible rather than silently dropped.
func policyConstraints() map[string]string {
	root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
	if root == "" {
		return nil
	}
	path := filepath.Join(root, filepath.FromSlash(DefaultPolicyRel))
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "policy %s: %v\n", path, err)
		}
		return nil
	}
	var rules map[string]string
	if err := json.Unmarshal(raw, &rules); err != nil {
		fmt.Fprintf(os.Stderr, "policy %s: %v\n", path, err)
		return nil
	}
	out := make(map[string]string, len(rules))
	for key, value := range rules {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		out[key] = value
	}
	return out
}
