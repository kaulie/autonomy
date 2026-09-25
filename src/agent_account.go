package autonomy

import (
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The pool is where an agent's credentials, model and workspace root come from — the
// runtime no longer reads any of them out of the environment (src/accounts.go). *Which* account
// an agent runs on is decided in one place (assignAgentRuntime, src/agent_runtime.go); this file
// is the other half: what the agent takes from the account it was given.

// adoptAccount records what an agent takes from its account: the harness it runs on, the
// model and workspace root it works with, and the credential its session is built from.
//
// The credential is held in memory for the session to read through Facts.Creds; the account
// id is what the row keeps (so a restart resumes the same entries), and it is persisted by
// the caller once the agent's session is attached.
func (a *Agent) adoptAccount(account *Account) {
	if a == nil || account == nil {
		return
	}
	a.AccountID = account.ID
	a.accountLabel = account.Label
	a.credential = llmbackend.Creds{
		Harness: account.Harness,
		Vendor:  account.Vendor,
		APIKey:  account.APIKey,
		BaseURL: account.BaseURL,
		Model:   account.Model,
	}
	if backend := llmbackend.Backend(account.Harness); backend != "" {
		a.Backend = backend
	}
	// The account's model is the choice; an account without one leaves the model to its
	// harness's own default, which is what a key-less account on the provider's saved auth
	// wants.
	if account.Model != "" {
		a.Model = account.Model
	}
	// An account owns its workspace root exclusively, and each of its agents gets its own
	// directory under it — the rule the runtime's default root already follows, so two
	// accounts' agents can never end up in one directory. An agent something put somewhere
	// else on purpose keeps that directory (Agent.workspaceChosen).
	if root := strings.TrimSpace(account.WorkspaceRoot); root != "" && strings.TrimSpace(a.Name) != "" && !a.workspaceChosen {
		if workspace, err := ensureAgentWorkspaceIn(root, a.Name); err == nil {
			a.Workspace = workspace
		} else {
			a.Workspace = AgentWorkspacePathIn(root, a.Name)
		}
	}
}

// accountSummary is what a status or log line says about where an agent bills: the account
// it resolved, in one string.
func (a *Agent) accountSummary() string {
	if a == nil || a.AccountID == "" {
		return ""
	}
	// A row read back from the store knows the account id but not its label (the label lives in
	// the pool, and the backend is runtime state that is not persisted): what a status can
	// always say is the id, and everything else it happens to hold.
	label := a.accountLabel
	if label == "" {
		return a.AccountID
	}
	if a.Backend == "" || a.Backend == llmbackend.Local {
		return fmt.Sprintf("%s (%s)", a.AccountID, label)
	}
	return fmt.Sprintf("%s (%s/%s)", a.AccountID, a.Backend, label)
}
