package autonomy

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// The pool's API-shaped surface (docs/http-api.md「账号池」): accounts rendered with a mask
// instead of a key, and the one probe that answers "is this entry usable".

// AccountView is one account as an API renders it. It embeds the domain type — whose APIKey
// is `json:"-"`, so the secret cannot be rendered even by accident — and adds the mask plus
// whether a key is stored at all (an account without one runs on the provider's own saved
// auth, the normal case for a machine that already ran `cline auth` / `codex auth`).
type AccountView struct {
	Account
	APIKeyMasked string `json:"apiKeyMasked"`
	HasKey       bool   `json:"hasKey"`
}

// AccountInput is one account as a request body: the writable subset. The id, the timestamps
// and the default rule belong to the store (src/db/sqlite_accounts.go).
type AccountInput struct {
	Harness       string `json:"harness"`
	Vendor        string `json:"vendor"`
	Label         string `json:"label"`
	APIKey        string `json:"apiKey"`
	BaseURL       string `json:"baseUrl"`
	Model         string `json:"model"`
	WorkspaceRoot string `json:"agentRootWorkspace"`
	Enabled       *bool  `json:"enabled"`
	IsDefault     *bool  `json:"isDefault"`
}

// AccountVerification is what one probe of one account found.
type AccountVerification struct {
	AccountID string `json:"accountId"`
	Harness   string `json:"harness"`
	Vendor    string `json:"vendor"`
	OK        bool   `json:"ok"`
	// Load is whether the harness's bridge handshaken; Live whether a turn was actually run
	// (which spends a little of that account's quota).
	Load   bool   `json:"load"`
	Live   bool   `json:"live"`
	Detail string `json:"detail"`
	Model  string `json:"model,omitempty"`
	Text   string `json:"text,omitempty"`
}

// viewAccount renders one account with its mask.
func viewAccount(account Account) AccountView {
	return AccountView{
		Account:      account,
		APIKeyMasked: MaskAPIKey(account.APIKey),
		HasKey:       strings.TrimSpace(account.APIKey) != "",
	}
}

func (r *Autonomy) accountStoreOrErr() (AccountStore, error) {
	if r == nil || r.Store == nil {
		return nil, fmt.Errorf("store not ready")
	}
	return r.Store, nil
}

// AccountViews reads the pool, masked: what the accounts page and a task dialog show.
func (r *Autonomy) AccountViews(filter AccountFilter) ([]AccountView, error) {
	store, err := r.accountStoreOrErr()
	if err != nil {
		return nil, err
	}
	accounts, err := store.ListAccounts(filter)
	if err != nil {
		return nil, err
	}
	views := make([]AccountView, 0, len(accounts))
	for _, account := range accounts {
		views = append(views, viewAccount(account))
	}
	return views, nil
}

// AddAccount writes one account into the pool.
func (r *Autonomy) AddAccount(input AccountInput) (AccountView, error) {
	store, err := r.accountStoreOrErr()
	if err != nil {
		return AccountView{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	isDefault := false
	if input.IsDefault != nil {
		isDefault = *input.IsDefault
	}
	account, err := store.CreateAccount(Account{
		Harness: input.Harness, Vendor: input.Vendor, Label: input.Label, APIKey: input.APIKey,
		BaseURL: input.BaseURL, Model: input.Model, WorkspaceRoot: input.WorkspaceRoot,
		Enabled: enabled, IsDefault: isDefault,
	})
	if err != nil {
		return AccountView{}, err
	}
	return viewAccount(account), nil
}

// EditAccount patches one account: a field the input does not carry is left alone, which is
// what lets a UI edit one label without restating the rest (and without a key, which it
// never sees).
func (r *Autonomy) EditAccount(accountID string, input AccountInput) (AccountView, error) {
	store, err := r.accountStoreOrErr()
	if err != nil {
		return AccountView{}, err
	}
	patch := AccountPatch{Enabled: input.Enabled, IsDefault: input.IsDefault}
	for _, edit := range []struct {
		value string
		into  **string
	}{
		{input.Harness, &patch.Harness},
		{input.Vendor, &patch.Vendor},
		{input.Label, &patch.Label},
		{input.APIKey, &patch.APIKey},
		{input.BaseURL, &patch.BaseURL},
		{input.Model, &patch.Model},
		{input.WorkspaceRoot, &patch.WorkspaceRoot},
	} {
		if edit.value != "" {
			value := edit.value
			*edit.into = &value
		}
	}
	account, err := store.UpdateAccount(accountID, patch)
	if err != nil {
		return AccountView{}, err
	}
	return viewAccount(account), nil
}

// RemoveAccount deletes one account from the pool.
func (r *Autonomy) RemoveAccount(accountID string) error {
	store, err := r.accountStoreOrErr()
	if err != nil {
		return err
	}
	return store.DeleteAccount(accountID)
}

// DefaultAccount is the account a harness would resolve to right now: its default, else the
// first enabled one. nil when the pool has nothing for that harness — which is exactly the
// state /health should be able to show, together with the model a run would use.
func (r *Autonomy) DefaultAccount(backend llmbackend.Backend) (*Account, error) {
	store, err := r.accountStoreOrErr()
	if err != nil {
		return nil, err
	}
	enabled := true
	pool, err := store.ListAccounts(AccountFilter{Harness: string(backend), Enabled: &enabled})
	if err != nil {
		return nil, err
	}
	for i := range pool {
		if pool[i].IsDefault {
			return &pool[i], nil
		}
	}
	if len(pool) > 0 {
		return &pool[0], nil
	}
	return nil, nil
}

// VerifyAccount probes one account. Without live it only loads the harness's bridge (free);
// with it, the account's own credentials answer one short turn. The point is that "is this
// entry usable" is answerable before a task is handed to it, instead of failing at that
// task's first cycle.
func (r *Autonomy) VerifyAccount(ctx context.Context, accountID string, live bool) (AccountVerification, error) {
	store, err := r.accountStoreOrErr()
	if err != nil {
		return AccountVerification{}, err
	}
	account, err := store.GetAccount(accountID)
	if err != nil {
		return AccountVerification{}, err
	}
	if account == nil {
		return AccountVerification{}, fmt.Errorf("account %s not found", strings.TrimSpace(accountID))
	}
	verification := AccountVerification{
		AccountID: account.ID, Harness: account.Harness, Vendor: account.Vendor, Model: account.Model,
	}
	probed, err := llmbackend.ProbeHarness(ctx, llmbackend.Backend(account.Harness), llmbackend.Creds{
		Harness: account.Harness, Vendor: account.Vendor, APIKey: account.APIKey,
		BaseURL: account.BaseURL, Model: account.Model,
	}, live)
	if err != nil {
		verification.Detail = err.Error()
		return verification, nil
	}
	verification.OK = true
	verification.Load = probed.Load
	verification.Live = probed.Live
	verification.Detail = probed.Detail
	verification.Text = probed.Text
	if probed.Model != "" {
		verification.Model = probed.Model
	}
	return verification, nil
}
