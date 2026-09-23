package db

import . "github.com/kaulie/autonomy/src"

import (
	"context"
	"fmt"
	"strings"
)

// AccountStore, as the sqlite engine serves it: the harness credential pool
// (src/accounts.go). The api_key column is the only secret this database holds, and it
// leaves the port only through GetAccount — the HTTP layer masks it before rendering.

const accountsDDL = `
CREATE TABLE IF NOT EXISTS provider_accounts (
  account_id     TEXT PRIMARY KEY,
  harness        TEXT NOT NULL,
  vendor         TEXT NOT NULL,
  label          TEXT NOT NULL,
  api_key        TEXT NOT NULL DEFAULT '',
  base_url       TEXT NOT NULL DEFAULT '',
  model          TEXT NOT NULL DEFAULT '',
  workspace_root TEXT NOT NULL DEFAULT '',
  enabled        INTEGER NOT NULL DEFAULT 1,
  is_default     INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_provider_accounts_harness
  ON provider_accounts(harness, vendor, enabled);
`

const accountCols = `account_id, harness, vendor, label, api_key, base_url, model, workspace_root, enabled, is_default, created_at, updated_at`

// accountsWorkspaceIndex makes "one account per root" a fact of the schema as well as a check
// in the writes below. It is created separately from the table because an older database may
// already hold two accounts on one root (the pool was not exclusive when it was introduced):
// failing to build the index must not keep a runtime from starting, and the write-time check
// below keeps new claims exclusive either way.
func (s *SQLiteStore) ensureAccountsWorkspaceIndex() error {
	_, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_accounts_workspace
  ON provider_accounts(workspace_root)`)
	return err
}

// ensureWorkspaceRootFree rejects a root another account already claims. Empty is a claim too:
// "the runtime's default root" belongs to one account, so every other account must name its own.
func (s *SQLiteStore) ensureWorkspaceRootFree(root, accountID string) error {
	var otherID, otherLabel string
	err := s.db.QueryRow(`SELECT account_id, label FROM provider_accounts
WHERE workspace_root = ? AND account_id <> ? LIMIT 1`, root, accountID).Scan(&otherID, &otherLabel)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil
		}
		return err
	}
	return WorkspaceClaimedErr(root, otherID, otherLabel)
}

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var (
		account          Account
		created, updated string
	)
	if err := sc.Scan(&account.ID, &account.Harness, &account.Vendor, &account.Label, &account.APIKey,
		&account.BaseURL, &account.Model, &account.WorkspaceRoot, &account.Enabled, &account.IsDefault,
		&created, &updated); err != nil {
		return Account{}, err
	}
	account.CreatedAt = parseTime(created)
	account.UpdatedAt = parseTime(updated)
	return account, nil
}

// accountWhere builds the filter's WHERE clause; the order is the one a UI shows and a
// resolution walks: harness, vendor, the default first, then oldest first.
func accountWhere(filter AccountFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	if harness := strings.TrimSpace(filter.Harness); harness != "" {
		clauses = append(clauses, "harness = ?")
		args = append(args, harness)
	}
	if vendor := strings.TrimSpace(filter.Vendor); vendor != "" {
		clauses = append(clauses, "vendor = ?")
		args = append(args, vendor)
	}
	if filter.Enabled != nil {
		clauses = append(clauses, "enabled = ?")
		args = append(args, *filter.Enabled)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *SQLiteStore) ListAccounts(filter AccountFilter) ([]Account, error) {
	where, args := accountWhere(filter)
	rows, err := s.db.Query(`SELECT `+accountCols+` FROM provider_accounts`+where+
		` ORDER BY harness, vendor, is_default DESC, created_at ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetAccount(id string) (*Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM provider_accounts WHERE account_id = ?`, strings.TrimSpace(id))
	account, err := scanAccount(row)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, fmt.Errorf("get account: %w", err)
	}
	return &account, nil
}

// CreateAccount writes a new account, making it its harness's default when nothing else is:
// a pool with one account needs no ceremony to be usable.
func (s *SQLiteStore) CreateAccount(account Account) (Account, error) {
	account, err := NormalizeAccount(account)
	if err != nil {
		return Account{}, err
	}
	if err := s.ensureWorkspaceRootFree(account.WorkspaceRoot, account.ID); err != nil {
		return Account{}, err
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var siblings int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provider_accounts WHERE harness = ? AND is_default = 1`,
		account.Harness).Scan(&siblings); err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	if !account.IsDefault && siblings == 0 {
		account.IsDefault = true
	}
	if account.IsDefault {
		if _, err := tx.ExecContext(ctx,
			`UPDATE provider_accounts SET is_default = 0 WHERE harness = ?`, account.Harness); err != nil {
			return Account{}, fmt.Errorf("create account: clear defaults: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO provider_accounts (`+accountCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		account.ID, account.Harness, account.Vendor, account.Label, account.APIKey, account.BaseURL,
		account.Model, account.WorkspaceRoot, account.Enabled, account.IsDefault,
		formatTime(account.CreatedAt), formatTime(account.UpdatedAt)); err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	return account, nil
}

// UpdateAccount patches one account. A field left nil is left alone; the result goes through
// the same normalization a create does, so a patch cannot produce an account a create would
// have refused. Making an account the default clears that flag on the others of its harness.
func (s *SQLiteStore) UpdateAccount(id string, patch AccountPatch) (Account, error) {
	current, err := s.GetAccount(id)
	if err != nil {
		return Account{}, err
	}
	if current == nil {
		return Account{}, fmt.Errorf("update account: %s not found", strings.TrimSpace(id))
	}
	next := *current
	if patch.Harness != nil {
		next.Harness = *patch.Harness
	}
	if patch.Vendor != nil {
		next.Vendor = *patch.Vendor
	}
	if patch.Label != nil {
		next.Label = *patch.Label
	}
	if patch.APIKey != nil {
		next.APIKey = *patch.APIKey
	}
	if patch.BaseURL != nil {
		next.BaseURL = *patch.BaseURL
	}
	if patch.Model != nil {
		next.Model = *patch.Model
	}
	if patch.WorkspaceRoot != nil {
		next.WorkspaceRoot = *patch.WorkspaceRoot
	}
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
	}
	if patch.IsDefault != nil {
		next.IsDefault = *patch.IsDefault
	}
	next, err = NormalizeAccount(next)
	if err != nil {
		return Account{}, err
	}
	if err := s.ensureWorkspaceRootFree(next.WorkspaceRoot, next.ID); err != nil {
		return Account{}, err
	}
	next.CreatedAt = current.CreatedAt

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if next.IsDefault {
		if _, err := tx.ExecContext(ctx,
			`UPDATE provider_accounts SET is_default = 0 WHERE harness = ? AND account_id <> ?`,
			next.Harness, next.ID); err != nil {
			return Account{}, fmt.Errorf("update account: clear defaults: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE provider_accounts SET harness = ?, vendor = ?, label = ?, api_key = ?, base_url = ?,
		 model = ?, workspace_root = ?, enabled = ?, is_default = ?, updated_at = ? WHERE account_id = ?`,
		next.Harness, next.Vendor, next.Label, next.APIKey, next.BaseURL, next.Model,
		next.WorkspaceRoot, next.Enabled, next.IsDefault, formatTime(next.UpdatedAt), next.ID); err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	return next, nil
}

// DeleteAccount removes an account. Agents that recorded it are not rewritten: the next
// resolution simply falls back to the pool for their harness (src/agent_backend.go).
func (s *SQLiteStore) DeleteAccount(id string) error {
	if _, err := s.db.Exec(`DELETE FROM provider_accounts WHERE account_id = ?`, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}
