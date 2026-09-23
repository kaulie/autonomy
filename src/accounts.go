package autonomy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaulie/autonomy/src/llmbackend"
)

// Accounts: the harness account pool.
//
// An account is **one harness + one vendor + one credential** — not "the cursor key", and
// never an environment variable. A runtime resolves an account for each agent out of this
// pool (src/agent_backend.go), which is what replaces injecting keys through the
// environment: a deployment's .env carries paths, ports and the store DSN, nothing a run
// bills.
//
// The three harnesses differ in what a vendor means:
//   - cursor: no second level — the vendor is "cursor" itself;
//   - cline:  the vendor is the LLM maker the key belongs to (deepseek, minimax, …);
//   - codex:  "openai" (the Codex CLI's own endpoint), or whatever a base URL says.
//
// A credential is optional: an account with no key runs on the provider's own saved auth
// (`cline auth`, `codex auth`), which is how a machine that already authenticated gets a
// pool entry without pasting a secret.
type Account struct {
	ID      string `json:"accountId"`
	Harness string `json:"harness"`
	Vendor  string `json:"vendor"`
	Label   string `json:"label"`
	// APIKey only ever lives in the store: it is never rendered, only masked
	// (src/http_server.go), so a list response cannot leak it.
	APIKey  string `json:"-"`
	BaseURL string `json:"baseUrl,omitempty"`
	// Model is the model this account runs ("" = the harness's own default).
	Model string `json:"model,omitempty"`
	// WorkspaceRoot is where agents on this account work ("" = the runtime's default).
	WorkspaceRoot string `json:"agentRootWorkspace,omitempty"`
	Enabled       bool   `json:"enabled"`
	// IsDefault makes this the pool's first choice for its harness.
	IsDefault bool      `json:"isDefault"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CursorVendor is the only vendor a cursor account can have: Cursor has no second level.
const CursorVendor = "cursor"

// DefaultVendorFor is the vendor a harness falls back to when the caller names none.
func DefaultVendorFor(harness string) string {
	switch normalizeHarness(harness) {
	case string(llmbackend.Cursor):
		return CursorVendor
	case string(llmbackend.Codex):
		return "openai"
	default:
		return "deepseek"
	}
}

// AccountHarnesses are the harnesses the pool can hold credentials for, in a stable order:
// what the UI offers and what validation accepts.
func AccountHarnesses() []string {
	return []string{string(llmbackend.Cursor), string(llmbackend.Cline), string(llmbackend.Codex)}
}

// normalizeHarness maps a harness name (and the aliases a caller may use) onto the backend
// token, "" when it is not one this runtime can run.
func normalizeHarness(harness string) string {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case string(llmbackend.Cursor), "cursor_sdk":
		return string(llmbackend.Cursor)
	case string(llmbackend.Cline), "cline_sdk":
		return string(llmbackend.Cline)
	case string(llmbackend.Codex), "codex_sdk":
		return string(llmbackend.Codex)
	default:
		return ""
	}
}

// NormalizeAccount fills in what an input may leave out and reports what it cannot accept:
// the harness must be one this runtime runs, the label must say something a human
// recognizes, and a cline account must name the vendor whose key it holds (a cline key is
// meaningless without one).
func NormalizeAccount(account Account) (Account, error) {
	account.Harness = normalizeHarness(account.Harness)
	if account.Harness == "" {
		return Account{}, fmt.Errorf("unknown account harness %q (supported: %s)",
			strings.TrimSpace(account.Harness), strings.Join(AccountHarnesses(), ", "))
	}
	account.Label = strings.TrimSpace(account.Label)
	if account.Label == "" {
		return Account{}, fmt.Errorf("account label is required")
	}
	account.Vendor = strings.TrimSpace(account.Vendor)
	if account.Vendor == "" {
		account.Vendor = DefaultVendorFor(account.Harness)
	}
	account.ID = strings.TrimSpace(account.ID)
	if account.ID == "" {
		account.ID = NewAccountID()
	}
	account.APIKey = strings.TrimSpace(account.APIKey)
	account.BaseURL = strings.TrimSpace(account.BaseURL)
	account.Model = strings.TrimSpace(account.Model)
	// The workspace root is exclusive: cleaned, so "…/a/" and "…/a" are the same claim, and
	// empty is itself a claim ("the runtime's default root") that only one account may make
	// (src/db/sqlite_accounts.go).
	account.WorkspaceRoot = strings.TrimSpace(account.WorkspaceRoot)
	if account.WorkspaceRoot != "" {
		account.WorkspaceRoot = filepath.Clean(account.WorkspaceRoot)
	}
	if account.CreatedAt.IsZero() {
		account.CreatedAt = time.Now()
	}
	account.UpdatedAt = time.Now()
	return account, nil
}

// WorkspaceClaimedErr is the refusal a pool write gets when the workspace root it asks for is
// already another account's. Roots are exclusive — including the empty one, which means "the
// runtime's default root" — because an account's agents work under its own root, and two
// accounts sharing one would put different tenants' agents in the same place.
func WorkspaceClaimedErr(root, accountID, label string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("the runtime's default agent root is already claimed by account %s (%s): give this account its own agent_root_workspace", accountID, label)
	}
	return fmt.Errorf("agent_root_workspace %s is already claimed by account %s (%s): each account owns its own root, so two accounts' agents cannot share a directory", root, accountID, label)
}

// MaskAPIKey is how a key is shown to a human: the first and last few characters, and
// nothing for a key too short to hide anything in.
func MaskAPIKey(apiKey string) string {
	raw := strings.TrimSpace(apiKey)
	if raw == "" {
		return ""
	}
	if len(raw) <= 8 {
		return "••••"
	}
	return raw[:4] + "…" + raw[len(raw)-4:]
}

// NewAccountID mints one account id.
func NewAccountID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("acct-%d", time.Now().UnixNano())
	}
	return "acct-" + hex.EncodeToString(b[:])
}
