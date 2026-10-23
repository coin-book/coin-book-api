//go:build integration

package acceptance

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsersSeeOnlyTheirOwnData(t *testing.T) {
	alice := newClient(t)
	bob := alice.signUp("bob@coinbook.local")

	var imported importResult
	alice.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, &imported)
	var token struct {
		Token struct {
			ID string `json:"id"`
		} `json:"token"`
	}
	alice.do(http.MethodPost, "/api/v1/agent-tokens", map[string]any{"name": "asistent", "scopes": []string{"transactions:read"}}, http.StatusCreated, &token)
	alice.createCategory(map[string]any{"name": "Kolo", "kind": "expense"})
	alice.do(http.MethodPut, "/api/v1/settings", map[string]any{"auto_categorize_threshold": 0.8}, http.StatusOK, nil)

	var accounts struct {
		Accounts []any `json:"accounts"`
	}
	bob.do(http.MethodGet, "/api/v1/accounts", nil, http.StatusOK, &accounts)
	assert.Empty(t, accounts.Accounts)
	assert.Empty(t, bob.transactions("").Transactions)
	var tokens struct {
		Tokens []any `json:"tokens"`
	}
	bob.do(http.MethodGet, "/api/v1/agent-tokens", nil, http.StatusOK, &tokens)
	assert.Empty(t, tokens.Tokens)
	assert.NotContains(t, bob.taxonomy(), "kolo")
	var settings struct {
		Threshold float64 `json:"auto_categorize_threshold"`
	}
	bob.do(http.MethodGet, "/api/v1/settings", nil, http.StatusOK, &settings)
	assert.Zero(t, settings.Threshold)

	aliceTx := alice.transactions("").Transactions[0].ID
	bob.do(http.MethodGet, "/api/v1/transactions/"+aliceTx, nil, http.StatusNotFound, nil)
	bob.do(http.MethodDelete, "/api/v1/agent-tokens/"+token.Token.ID, nil, http.StatusNotFound, nil)

	// The same bank account imported by another user is their own copy, not a duplicate.
	var again importResult
	bob.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, &again)
	assert.Equal(t, len(octoberRows), again.RowsNew)
	assert.Len(t, alice.transactions("").Transactions, len(octoberRows))
	assert.Len(t, bob.transactions("").Transactions, len(octoberRows))
}

func TestAgentTokenReadsItsOwnersDataOnly(t *testing.T) {
	alice := newClient(t)
	bob := alice.signUp("bob@coinbook.local")
	alice.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)

	var created struct {
		Secret string `json:"secret"`
	}
	bob.do(http.MethodPost, "/api/v1/agent-tokens", map[string]any{"name": "bobuv", "scopes": []string{"transactions:read"}}, http.StatusCreated, &created)
	agent := bob.withToken(created.Secret)
	assert.Empty(t, agent.transactions("").Transactions)

	var audit struct {
		Entries []any `json:"entries"`
	}
	alice.do(http.MethodGet, "/api/v1/audit-log", nil, http.StatusOK, &audit)
	assert.Empty(t, audit.Entries, "another user's agent does not show up in the audit log")
	bob.do(http.MethodGet, "/api/v1/audit-log", nil, http.StatusOK, &audit)
	assert.Len(t, audit.Entries, 1)
}

func TestEveryUserTableIsProtectedByRowSecurity(t *testing.T) {
	newClient(t)
	ctx := context.Background()
	// Reference data and the tables the API reads before it knows the user are the only exceptions.
	rows, err := pg.Pool.Query(ctx, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND NOT (c.relrowsecurity AND c.relforcerowsecurity)
		  AND c.relname NOT IN ('app_user', 'category', 'budget_group', 'bank', 'agent_token', 'audit_log', 'schema_migrations',
		    'instrument', 'price_series', 'price_bar', 'fx_rate')
		ORDER BY 1`)
	require.NoError(t, err)
	unprotected, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	assert.Empty(t, unprotected)

	var bypasses bool
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypasses))
	assert.False(t, bypasses, "the API must not query as a role that bypasses row security")

	var visible int
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT count(*) FROM app_user`).Scan(&visible))
	assert.Equal(t, 1, visible)
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT count(*) FROM account`).Scan(&visible))
	assert.Zero(t, visible, "without a user scope the policies hide every row")
}

// Link rows (categories, transfers) carry their owner; writing one onto another user's
// transaction or account, or under another owner, must be refused by row-level security.
func TestLinkRowsCannotReachAnotherUsersTransactions(t *testing.T) {
	alice := newClient(t)
	bob := alice.signUp("bob@coinbook.local")
	alice.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	bob.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)

	ctx := context.Background()
	owner, err := pgx.Connect(ctx, pg.DSN)
	require.NoError(t, err)
	defer owner.Close(ctx)
	var aliceID, bobID uuid.UUID
	require.NoError(t, owner.QueryRow(ctx, `SELECT id FROM app_user WHERE email = 'bob@coinbook.local'`).Scan(&bobID))
	require.NoError(t, owner.QueryRow(ctx, `SELECT id FROM app_user WHERE email <> 'bob@coinbook.local'`).Scan(&aliceID))
	var aliceTx, bobTx, bobOtherTx string
	var aliceAccount, bobAccount uuid.UUID
	require.NoError(t, owner.QueryRow(ctx, `SELECT id, account_id FROM transaction WHERE user_id = $1 LIMIT 1`, aliceID).Scan(&aliceTx, &aliceAccount))
	require.NoError(t, owner.QueryRow(ctx, `SELECT min(id), max(id) FROM transaction WHERE user_id = $1`, bobID).Scan(&bobTx, &bobOtherTx))
	require.NoError(t, owner.QueryRow(ctx, `SELECT account_id FROM transaction WHERE id = $1`, bobTx).Scan(&bobAccount))

	_, err = owner.Exec(ctx, `INSERT INTO instrument (isin, name) VALUES ('US0378331005', 'Apple') ON CONFLICT DO NOTHING`)
	require.NoError(t, err)
	bobCtx, release, err := pg.Pool.ScopeToUser(ctx, bobID)
	require.NoError(t, err)
	defer release()
	refused := map[string][]any{
		`INSERT INTO not_transfer (transaction_id) VALUES ($1)`:                                       {aliceTx},
		`INSERT INTO not_transfer (transaction_id, user_id) VALUES ($1, $2)`:                          {bobTx, aliceID},
		`INSERT INTO transfer_pair (out_tx_id, in_tx_id, matched_by) VALUES ($1, $2, 'manual')`:       {bobTx, aliceTx},
		`INSERT INTO transfer_pair (out_tx_id, in_tx_id, matched_by) VALUES ($2, $1, 'manual')`:       {bobTx, aliceTx},
		`INSERT INTO own_transfer (transaction_id, account_id, matched_by) VALUES ($1, $2, 'manual')`: {bobTx, aliceAccount},
		`INSERT INTO category_assignment (transaction_id, system_category_id, amount_minor, source)
			SELECT $1, id, 1, 'manual' FROM category WHERE parent_id IS NOT NULL LIMIT 1`: {aliceTx},
		`INSERT INTO investment_tx (id, account_id, isin, kind, traded_on, quantity, currency, gross_minor, net_minor)
			VALUES ('man_probe', $1, 'US0378331005', 'buy', '2021-12-21', 1, 'USD', 100, 100)`: {aliceAccount},
	}
	for sql, args := range refused {
		_, err := pg.Pool.Exec(bobCtx, sql, args...)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, sql)
		assert.Equal(t, "42501", pgErr.Code, sql)
	}

	_, err = pg.Pool.Exec(bobCtx, `INSERT INTO own_transfer (transaction_id, account_id, matched_by) VALUES ($1, $2, 'manual')`, bobOtherTx, bobAccount)
	require.NoError(t, err, "a one-leg transfer within the user's own data")
	var aliceRows int
	require.NoError(t, pg.Pool.QueryRow(bobCtx, `
		SELECT (SELECT count(*) FROM category_assignment WHERE user_id = $1) + (SELECT count(*) FROM own_transfer WHERE user_id = $1)`,
		aliceID).Scan(&aliceRows))
	assert.Zero(t, aliceRows)
}
