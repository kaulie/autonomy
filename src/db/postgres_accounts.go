package db

import . "github.com/kaulie/autonomy/src"

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AccountStore, as the postgres engine serves it: the same pool as the sqlite engine
// (src/db/sqlite_accounts.go), with this engine's own types — timestamps as TIMESTAMPTZ and
// the two flags as BOOLEAN instead of INTEGER.

const pgAccountsDDL = `
CREATE TABLE IF NOT EXISTS provider_accounts (
  account_id     TEXT PRIMARY KEY,
  harness        TEXT NOT NULL,
  vendor         TEXT NOT NULL,
  label          TEXT NOT NULL,
  api_key        TEXT NOT NULL DEFAULT '',
  base_url       TEXT NOT NULL DEFAULT '',
  model          TEXT NOT NULL DEFAULT '',
  workspace_root TEXT NOT NULL DEFAULT '',
  enabled        BOOLEAN NOT NULL DEFAULT TRUE,
  is_default     BOOLEAN NOT NULL DEFAULT FALSE,
  created_at     TIMESTAMPTZ NOT NULL,
  updated_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_provider_accounts_harness
  ON provider_accounts(harness, vendor, enabled);
`

// ensureAccountsWorkspaceIndex makes "one account per root" a fact of the schema, created
// separately from the table so a database that predates the rule (and may hold two accounts on
// one root) still starts: the write-time check below is what keeps new claims exclusive.
func (s *PostgresStore) ensureAccountsWorkspaceIndex() error {
	_, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_accounts_workspace
  ON provider_accounts(workspace_root)`)
	return err
}

// ensureWorkspaceRootFree rejects a root another account already claims (empty included: it means
// "the runtime's default root", which belongs to one account).
func (s *PostgresStore) ensureWorkspaceRootFree(root, accountID string) error {
	var otherID, otherLabel string
	err := s.db.QueryRow(`SELECT account_id, label FROM provider_accounts
WHERE workspace_root = $1 AND account_id <> $2 LIMIT 1`, root, accountID).Scan(&otherID, &otherLabel)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil
		}
		return err
	}
	return WorkspaceClaimedErr(root, otherID, otherLabel)
}

func scanPgAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var (
		account          Account
		created, updated time.Time
	)
	if err := sc.Scan(&account.ID, &account.Harness, &account.Vendor, &account.Label, &account.APIKey,
		&account.BaseURL, &account.Model, &account.WorkspaceRoot, &account.Enabled, &account.IsDefault,
		&created, &updated); err != nil {
		return Account{}, err
	}
	account.CreatedAt = created.UTC()
	account.UpdatedAt = updated.UTC()
	return account, nil
}

// pgAccountWhere is accountWhere with this engine's placeholders.
func pgAccountWhere(filter AccountFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if harness := strings.TrimSpace(filter.Harness); harness != "" {
		add("harness = $%d", harness)
	}
	if vendor := strings.TrimSpace(filter.Vendor); vendor != "" {
		add("vendor = $%d", vendor)
	}
	if filter.Enabled != nil {
		add("enabled = $%d", *filter.Enabled)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *PostgresStore) ListAccounts(filter AccountFilter) ([]Account, error) {
	where, args := pgAccountWhere(filter)
	rows, err := s.db.Query(`SELECT `+accountCols+` FROM provider_accounts`+where+
		` ORDER BY harness, vendor, is_default DESC, created_at ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		account, err := scanPgAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetAccount(id string) (*Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM provider_accounts WHERE account_id = $1`, strings.TrimSpace(id))
	account, err := scanPgAccount(row)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, fmt.Errorf("get account: %w", err)
	}
	return &account, nil
}

// CreateAccount writes a new account, making it its harness's default when nothing else is.
func (s *PostgresStore) CreateAccount(account Account) (Account, error) {
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
		`SELECT COUNT(*) FROM provider_accounts WHERE harness = $1 AND is_default`, account.Harness).Scan(&siblings); err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	if !account.IsDefault && siblings == 0 {
		account.IsDefault = true
	}
	if account.IsDefault {
		if _, err := tx.ExecContext(ctx,
			`UPDATE provider_accounts SET is_default = FALSE WHERE harness = $1`, account.Harness); err != nil {
			return Account{}, fmt.Errorf("create account: clear defaults: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO provider_accounts (`+accountCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		account.ID, account.Harness, account.Vendor, account.Label, account.APIKey, account.BaseURL,
		account.Model, account.WorkspaceRoot, account.Enabled, account.IsDefault,
		pgTime(account.CreatedAt), pgTime(account.UpdatedAt)); err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	return account, nil
}

// UpdateAccount patches one account, through the same normalization a create uses.
func (s *PostgresStore) UpdateAccount(id string, patch AccountPatch) (Account, error) {
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
			`UPDATE provider_accounts SET is_default = FALSE WHERE harness = $1 AND account_id <> $2`,
			next.Harness, next.ID); err != nil {
			return Account{}, fmt.Errorf("update account: clear defaults: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE provider_accounts SET harness = $1, vendor = $2, label = $3, api_key = $4, base_url = $5,
		 model = $6, workspace_root = $7, enabled = $8, is_default = $9, updated_at = $10 WHERE account_id = $11`,
		next.Harness, next.Vendor, next.Label, next.APIKey, next.BaseURL, next.Model,
		next.WorkspaceRoot, next.Enabled, next.IsDefault, pgTime(next.UpdatedAt), next.ID); err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	return next, nil
}

// DeleteAccount removes an account.
func (s *PostgresStore) DeleteAccount(id string) error {
	if _, err := s.db.Exec(`DELETE FROM provider_accounts WHERE account_id = $1`, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}
