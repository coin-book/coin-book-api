//go:build integration

package acceptance

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
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
