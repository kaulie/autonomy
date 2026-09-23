package autonomy

import (
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The pool is where an agent's credentials, model and workspace root come from — the
// runtime no longer reads any of them out of the environment (src/accounts.go). This file is
// that sentence as a resolution: which account an agent runs on, and what it adopts from it.

// resolveAccountFor picks the pool entry an agent runs on.
//
//  1. the account the agent recorded (an explicit choice, e.g. from a task's account_id) —
//     and if that one is gone or disabled, that is said plainly instead of quietly running
//     somewhere else, because a task that named an account must not bill another;
//  2. otherwise the harness's default account, else the first enabled one — the pool's own
//     order (harness, vendor, default first, oldest first);
//  3. otherwise nothing can run: the error names the harness and points at /accounts.
func resolveAccountFor(agent *Agent) (*Account, error) {
	store := activeAccountStore()
	if store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	if agent == nil {
		return nil, fmt.Errorf("nil agent")
	}
	if id := strings.TrimSpace(agent.AccountID); id != "" {
		account, err := store.GetAccount(id)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, fmt.Errorf("account %s (recorded on %s) is gone: add one at /accounts", id, agent.Name)
		}
		if !account.Enabled {
			return nil, fmt.Errorf("account %s (%s) is disabled: enable it, or point the agent at another one, at /accounts", account.ID, account.Label)
		}
		return account, nil
	}
	enabled := true
	backend := agent.effectiveBackend()
	pool, err := store.ListAccounts(AccountFilter{Harness: string(backend), Enabled: &enabled})
	if err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return nil, fmt.Errorf("no enabled %s account in the pool: add one at /accounts (POST /api/accounts) — the runtime reads credentials from the pool, not from the environment", backend)
	}
	for i := range pool {
		if pool[i].IsDefault {
			return &pool[i], nil
		}
	}
	return &pool[0], nil
}

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
	if account.WorkspaceRoot != "" && strings.TrimSpace(a.Workspace) == "" {
		a.Workspace = account.WorkspaceRoot
	}
}

// accountSummary is what a status or log line says about where an agent bills: the account
// it resolved, in one string.
func (a *Agent) accountSummary() string {
	if a == nil || a.AccountID == "" {
		return ""
	}
	label := a.accountLabel
	if label == "" {
		label = a.AccountID
	}
	return fmt.Sprintf("%s (%s/%s)", a.AccountID, a.Backend, label)
}
