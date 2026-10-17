//go:build integration

package acceptance

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var savingsRows = []row{
	{"2026-10-03", "VLASTNÍ PŘEVOD  SPORENI TEST  000000 9876543210 6210", "Bez kategorie - jiné", -300000},
	{"2026-10-08", "KAVARNA U TESTU  PLATBA KARTOU", "Restaurace", -8900},
}

type legView struct {
	ID           string `json:"id"`
	Counterparty string `json:"counterparty"`
	Transfer     *struct {
		CounterpartTxID      *string `json:"counterpart_tx_id"`
		CounterpartAccountID string  `json:"counterpart_account_id"`
		MatchedBy            string  `json:"matched_by"`
	} `json:"transfer"`
}

func (c *client) legs() map[string]legView {
	c.t.Helper()
	var list struct {
		Transactions []legView `json:"transactions"`
	}
	c.do(http.MethodGet, "/api/v1/transactions?limit=200", nil, http.StatusOK, &list)
	byCounterparty := map[string]legView{}
	for _, tx := range list.Transactions {
		byCounterparty[tx.Counterparty] = tx
	}
	return byCounterparty
}

type monthlyReport struct {
	ExpenseMinor int64 `json:"expense_minor"`
	SetAside     []struct {
		AccountID   string `json:"account_id"`
		AmountMinor int64  `json:"amount_minor"`
	} `json:"set_aside"`
}

func TestPaymentToAnOwnAccountWithoutStatementIsATransfer(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, savingsRows), http.StatusCreated, nil)

	var candidates struct {
		Candidates []struct {
			AccountNumber string `json:"account_number"`
			BankID        string `json:"bank_id"`
			Transactions  int    `json:"transactions"`
			OutMinor      int64  `json:"out_minor"`
		} `json:"candidates"`
	}
	c.do(http.MethodGet, "/api/v1/transfers/own-account-candidates", nil, http.StatusOK, &candidates)
	require.Len(t, candidates.Candidates, 1)
	assert.Equal(t, "9876543210/6210", candidates.Candidates[0].AccountNumber)
	assert.Equal(t, "cz-6210", candidates.Candidates[0].BankID)
	assert.Equal(t, int64(300000), candidates.Candidates[0].OutMinor)

	var savings bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Spoření", "kind": "savings", "currency": "CZK",
		"account_number": "000000-9876543210/6210"}, http.StatusCreated, &savings)

	leg := c.legs()["SPORENI TEST"]
	require.NotNil(t, leg.Transfer, "the earlier payment is recognised once the account exists")
	assert.Equal(t, savings.ID, leg.Transfer.CounterpartAccountID)
	assert.Equal(t, "number", leg.Transfer.MatchedBy)
	assert.Nil(t, leg.Transfer.CounterpartTxID)

	c.do(http.MethodGet, "/api/v1/transfers/own-account-candidates", nil, http.StatusOK, &candidates)
	assert.Empty(t, candidates.Candidates)
	for _, tx := range c.transactions("uncategorized=true").Transactions {
		assert.NotEqual(t, leg.ID, tx.ID, "a transfer waits for no category")
	}

	var report monthlyReport
	c.do(http.MethodGet, "/api/v1/reports/monthly/2026-10", nil, http.StatusOK, &report)
	assert.Equal(t, int64(8900), report.ExpenseMinor)
	require.Len(t, report.SetAside, 1)
	assert.Equal(t, savings.ID, report.SetAside[0].AccountID)
	assert.Equal(t, int64(300000), report.SetAside[0].AmountMinor)

	c.do(http.MethodDelete, "/api/v1/transfers/"+leg.ID, nil, http.StatusNoContent, nil)
	c.upload("/api/v1/imports", "next.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-15", "PLATBA KARTOU  OBCHOD TEST", "Nákupy", -1000},
	}), http.StatusCreated, nil)
	assert.Nil(t, c.legs()["SPORENI TEST"].Transfer, "a transfer the user undid stays undone")
}

func TestMarkedOwnTransferFollowsTheCounterpartyAndBecomesAPair(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, append(octoberRows[:4:4],
		row{"2026-10-15", "ODCHOZÍ OKAMŽITÁ PLATBA  DOBITI REVOLUT", "Bez kategorie - jiné", -100000})), http.StatusCreated, nil)
	var wallet bankAccount
	c.do(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "Revolut ručně", "kind": "current", "currency": "CZK"},
		http.StatusCreated, &wallet)

	var topups []legView
	var list struct {
		Transactions []legView `json:"transactions"`
	}
	c.do(http.MethodGet, "/api/v1/transactions?limit=200&q=DOBITI", nil, http.StatusOK, &list)
	topups = list.Transactions
	require.Len(t, topups, 2)

	var marked struct {
		AppliedToOther int `json:"applied_to_other"`
	}
	c.do(http.MethodPut, "/api/v1/transfers/"+topups[0].ID+"/own", map[string]any{"account_id": wallet.ID,
		"remember_counterparty": true}, http.StatusOK, &marked)
	assert.Equal(t, 1, marked.AppliedToOther)
	c.do(http.MethodPut, "/api/v1/transfers/"+topups[0].ID+"/own", map[string]any{"account_id": c.bankAccounts()["mKonto Test"].ID},
		http.StatusUnprocessableEntity, nil)

	var revolut importResult
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(0, []row{{"2026-10-14", "Dobitie", "Dobiť účet", 500000}}), http.StatusCreated, &revolut)
	assert.Equal(t, 1, revolut.TransfersMatched)

	c.do(http.MethodGet, "/api/v1/transactions?limit=200&q=DOBITI", nil, http.StatusOK, &list)
	paired, oneLeg := 0, 0
	for _, tx := range list.Transactions {
		require.NotNil(t, tx.Transfer)
		if tx.Transfer.CounterpartTxID != nil {
			paired++
		} else {
			oneLeg++
		}
	}
	assert.Equal(t, 2, paired, "the imported other leg replaces the one-leg transfer; both legs match the search")
	assert.Equal(t, 1, oneLeg)
}
