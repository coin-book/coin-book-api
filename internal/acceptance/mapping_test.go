//go:build integration

package acceptance

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const genericCSV = "datum;castka;mena;popis;protistrana;kategorie\n" +
	"2026-10-14;-1 249,00;CZK;Nakup;ROHLIK.CZ;Potraviny\n" +
	"2026-10-15;-300;CZK;Obed;BISTRO;dining.restaurants\n"

type account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Currency string `json:"currency"`
	Balance  struct {
		Minor int64 `json:"minor"`
	} `json:"balance"`
	TransactionCount int `json:"transaction_count"`
}

func (c *client) accounts() []account {
	c.t.Helper()
	var out struct {
		Accounts []account `json:"accounts"`
	}
	c.do(http.MethodGet, "/api/v1/accounts", nil, http.StatusOK, &out)
	return out.Accounts
}

func (c *client) createAccount(name, kind, currency string) account {
	c.t.Helper()
	var a account
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": name, "kind": kind, "currency": currency}, http.StatusCreated, &a)
	return a
}

func TestGenericFileNeedsAnAccountAndKeepsItsCategories(t *testing.T) {
	c := newClient(t)

	var preview importResult
	c.upload("/api/v1/imports/preview", "pokladna.csv", []byte(genericCSV), http.StatusOK, &preview)
	require.Len(t, preview.Accounts, 1)
	assert.True(t, preview.Accounts[0].NeedsMapping)
	c.upload("/api/v1/imports", "pokladna.csv", []byte(genericCSV), http.StatusUnprocessableEntity, nil)

	key := preview.Accounts[0].Key
	var done importResult
	c.uploadMapped("/api/v1/imports", "pokladna.csv", []byte(genericCSV),
		map[string]any{key: map[string]any{"create": map[string]any{"name": "Hotovost", "kind": "cash"}}}, http.StatusCreated, &done)
	assert.Equal(t, 2, done.RowsNew)
	assert.Equal(t, 2, done.CategorizedFile)

	accounts := c.accounts()
	require.Len(t, accounts, 1)
	assert.Equal(t, "Hotovost", accounts[0].Name)
	assert.Equal(t, "cash", accounts[0].Kind)

	more := genericCSV + "2026-10-16;-50;CZK;Kava;;\n"
	var second importResult
	c.uploadMapped("/api/v1/imports", "pokladna-2.csv", []byte(more),
		map[string]any{key: map[string]any{"account_id": accounts[0].ID}}, http.StatusCreated, &second)
	assert.Equal(t, 1, second.RowsNew, "rows already in the account are recognised")
	assert.Equal(t, 2, second.RowsDuplicate)
}

func TestMappingABankAccountIsRemembered(t *testing.T) {
	c := newClient(t)
	main := c.createAccount("Hlavní účet", "current", "CZK")

	var preview importResult
	c.upload("/api/v1/imports/preview", "a.csv", mbankStatement(t, ownMBankAccount, octoberRows[:2]), http.StatusOK, &preview)
	require.Len(t, preview.Accounts, 1)
	assert.True(t, preview.Accounts[0].IsNew, "unknown number: suggest a new account")

	c.uploadMapped("/api/v1/imports", "a.csv", mbankStatement(t, ownMBankAccount, octoberRows[:2]),
		map[string]any{preview.Accounts[0].Key: map[string]any{"account_id": main.ID}}, http.StatusCreated, nil)

	var next importResult
	c.upload("/api/v1/imports", "b.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, &next)
	assert.Equal(t, "alias", next.Accounts[0].MatchedBy)
	assert.Equal(t, main.ID, *next.Accounts[0].AccountID)
	assert.Equal(t, 3, next.RowsNew)
	assert.Len(t, c.accounts(), 1)

	var aliases struct {
		Aliases []struct {
			ID       string `json:"id"`
			Identity string `json:"identity"`
		} `json:"aliases"`
	}
	c.do(http.MethodGet, "/api/v1/account-aliases", nil, http.StatusOK, &aliases)
	require.Len(t, aliases.Aliases, 1)
	c.do(http.MethodDelete, "/api/v1/account-aliases/"+aliases.Aliases[0].ID, nil, http.StatusNoContent, nil)
}

func TestMappingRefusesAnotherCurrency(t *testing.T) {
	c := newClient(t)
	czk := c.createAccount("Koruny", "current", "CZK")
	eur := "date,amount,currency,description\n2026-10-01,-12.50,EUR,Coffee\n"

	var preview importResult
	c.upload("/api/v1/imports/preview", "eur.csv", []byte(eur), http.StatusOK, &preview)
	c.uploadMapped("/api/v1/imports", "eur.csv", []byte(eur),
		map[string]any{preview.Accounts[0].Key: map[string]any{"account_id": czk.ID}}, http.StatusUnprocessableEntity, nil)
}

func TestMergeMovesTransactionsAndRedirectsFutureImports(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "a.csv", mbankStatement(t, ownMBankAccount, octoberRows[:3]), http.StatusCreated, nil)
	target := c.createAccount("Spojený účet", "current", "CZK")
	var source account
	for _, a := range c.accounts() {
		if a.ID != target.ID {
			source = a
		}
	}

	var merged struct {
		Moved int `json:"moved"`
	}
	c.do(http.MethodPost, "/api/v1/accounts/"+source.ID+"/merge", map[string]any{"target_id": target.ID}, http.StatusOK, &merged)
	assert.Equal(t, 3, merged.Moved)

	accounts := c.accounts()
	require.Len(t, accounts, 1)
	assert.Equal(t, target.ID, accounts[0].ID)
	assert.Equal(t, 3, accounts[0].TransactionCount)

	var next importResult
	c.upload("/api/v1/imports", "b.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, &next)
	assert.Equal(t, target.ID, *next.Accounts[0].AccountID)
	assert.Equal(t, 2, next.RowsNew)
	assert.Equal(t, 3, next.RowsDuplicate)

	c.do(http.MethodPost, "/api/v1/accounts/"+target.ID+"/merge", map[string]any{"target_id": target.ID}, http.StatusUnprocessableEntity, nil)
}

func TestMergeRefusesAnotherCurrency(t *testing.T) {
	c := newClient(t)
	a := c.createAccount("Koruny", "current", "CZK")
	b := c.createAccount("Eura", "current", "EUR")
	c.do(http.MethodPost, "/api/v1/accounts/"+a.ID+"/merge", map[string]any{"target_id": b.ID}, http.StatusUnprocessableEntity, nil)
	assert.Len(t, c.accounts(), 2)
}

func TestMergingManualAccountsLeavesNoAlias(t *testing.T) {
	c := newClient(t)
	a := c.createAccount("Peněženka", "cash", "CZK")
	b := c.createAccount("Hotovost", "cash", "CZK")
	c.do(http.MethodPost, "/api/v1/accounts/"+a.ID+"/merge", map[string]any{"target_id": b.ID}, http.StatusOK, nil)

	var aliases struct {
		Aliases []any `json:"aliases"`
	}
	c.do(http.MethodGet, "/api/v1/account-aliases", nil, http.StatusOK, &aliases)
	assert.Empty(t, aliases.Aliases)
}
