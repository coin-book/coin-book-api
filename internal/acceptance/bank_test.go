//go:build integration

package acceptance

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bankAccount struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	BankID *string `json:"bank_id"`
}

func (c *client) bankAccounts() map[string]bankAccount {
	c.t.Helper()
	var out struct {
		Accounts []bankAccount `json:"accounts"`
	}
	c.do(http.MethodGet, "/api/v1/accounts", nil, http.StatusOK, &out)
	byName := map[string]bankAccount{}
	for _, a := range out.Accounts {
		byName[a.Name] = a
	}
	return byName
}

func TestBankRegisterHoldsCzechSlovakAndFintechBanks(t *testing.T) {
	c := newClient(t)
	var out struct {
		Banks []struct {
			ID        string  `json:"id"`
			ShortName string  `json:"short_name"`
			BIC       *string `json:"bic"`
		} `json:"banks"`
	}
	c.do(http.MethodGet, "/api/v1/banks", nil, http.StatusOK, &out)
	byID := map[string]string{}
	for _, b := range out.Banks {
		byID[b.ID] = b.ShortName
	}
	assert.Equal(t, "Česká spořitelna", byID["cz-0800"])
	assert.Equal(t, "mBank", byID["cz-6210"])
	assert.Equal(t, "Tatra banka", byID["sk-1100"])
	assert.Equal(t, "Revolut", byID["revolut"])
	assert.Greater(t, len(out.Banks), 80)
}

func TestImportedAccountsKnowTheirBank(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows[:1]), http.StatusCreated, nil)
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(0, []row{{"2026-10-13", "Dobitie", "Dobiť účet", 500000}}), http.StatusCreated, nil)

	accounts := c.bankAccounts()
	require.Len(t, accounts, 2)
	for _, a := range accounts {
		require.NotNil(t, a.BankID, a.Name)
	}
	assert.Equal(t, "cz-6210", *accounts["mKonto Test"].BankID)
	assert.Equal(t, "revolut", *accounts["Revolut CZK"].BankID)
}

func TestManualAccountBankIsDetectedOrChosen(t *testing.T) {
	c := newClient(t)
	var detected, chosen bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Spořka", "kind": "current", "currency": "CZK",
		"account_number": "1234567890/0800"}, http.StatusCreated, &detected)
	require.NotNil(t, detected.BankID)
	assert.Equal(t, "cz-0800", *detected.BankID)

	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Wise", "kind": "current", "currency": "EUR",
		"bank_id": "wise"}, http.StatusCreated, &chosen)
	assert.Equal(t, "wise", *chosen.BankID)

	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "X", "kind": "current", "currency": "CZK",
		"bank_id": "no-such-bank"}, http.StatusUnprocessableEntity, nil)

	var updated bankAccount
	c.do(http.MethodPatch, "/api/v1/accounts/"+chosen.ID, map[string]any{"bank_id": ""}, http.StatusOK, &updated)
	assert.Nil(t, updated.BankID)
}
