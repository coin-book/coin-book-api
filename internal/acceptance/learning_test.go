//go:build integration

package acceptance

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var groceryHistory = []row{
	{"2026-09-02", "ALBERT PRAHA  PLATBA KARTOU", "Potraviny a drogerie", -45000},
	{"2026-09-09", "ALBERT HM  PLATBA KARTOU", "Potraviny a drogerie", -120000},
	{"2026-09-16", "LIDL DEKUJEME  PLATBA KARTOU", "Potraviny a drogerie", -80000},
	{"2026-09-20", "BISTRO NA ROHU  PLATBA KARTOU", "Restaurace", -29000},
	{"2026-09-22", "PIZZERIA TESTOVA  PLATBA KARTOU", "Restaurace", -35000},
}

type prediction struct {
	CategoryID string   `json:"category_id"`
	Confidence float64  `json:"confidence"`
	Reasons    []string `json:"reasons"`
}

func TestClassifierSuggestsForShopsItHasNotSeen(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "history.csv", mbankStatement(t, ownMBankAccount, groceryHistory), http.StatusCreated, nil)
	var revolut importResult
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(100000, []row{{"2026-10-13", "ALBERT BRNO", "Predajca", -30000}}), http.StatusCreated, &revolut)
	assert.Zero(t, revolut.CategorizedRule+revolut.CategorizedBank, "off by default: nothing categorised automatically")

	var triage struct {
		Transaction struct {
			Counterparty string `json:"counterparty"`
		} `json:"transaction"`
		Suggested []prediction `json:"suggested"`
	}
	c.do(http.MethodGet, "/api/v1/triage", nil, http.StatusOK, &triage)
	require.Equal(t, "ALBERT BRNO", triage.Transaction.Counterparty)
	require.NotEmpty(t, triage.Suggested)
	assert.Equal(t, c.categoryID("groceries"), triage.Suggested[0].CategoryID)
	assert.Contains(t, triage.Suggested[0].Reasons, "albert")

	var list struct {
		Transactions []struct {
			Suggestions []prediction `json:"suggestions"`
		} `json:"transactions"`
	}
	c.do(http.MethodGet, "/api/v1/transactions?uncategorized=true", nil, http.StatusOK, &list)
	require.Len(t, list.Transactions, 1)
	assert.Equal(t, c.categoryID("groceries"), list.Transactions[0].Suggestions[0].CategoryID)
}

// regularShopping is enough history for confident guesses: the same shops, again and again.
var regularShopping = append(append([]row{}, groceryHistory...),
	row{"2026-09-23", "ALBERT PRAHA  PLATBA KARTOU", "Potraviny a drogerie", -52000},
	row{"2026-09-25", "ALBERT HM  PLATBA KARTOU", "Potraviny a drogerie", -61000},
	row{"2026-09-27", "LIDL DEKUJEME  PLATBA KARTOU", "Potraviny a drogerie", -43000},
	row{"2026-09-29", "LIDL DEKUJEME  PLATBA KARTOU", "Potraviny a drogerie", -39000},
	row{"2026-09-30", "ALBERT PRAHA  PLATBA KARTOU", "Potraviny a drogerie", -47000},
)

func TestAutomaticCategoriesAboveThresholdAwaitConfirmation(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "history.csv", mbankStatement(t, ownMBankAccount, regularShopping), http.StatusCreated, nil)
	c.do(http.MethodPut, "/api/v1/settings", map[string]any{"auto_categorize_threshold": 0.2}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPut, "/api/v1/settings", map[string]any{"auto_categorize_threshold": 0.5}, http.StatusOK, nil)

	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(100000, []row{
		{"2026-10-13", "ALBERT BRNO", "Predajca", -30000},
		{"2026-10-14", "LIDL OSTRAVA", "Predajca", -25000},
	}), http.StatusCreated, nil)

	auto := c.transactions("auto=true")
	require.NotEmpty(t, auto.Transactions, "a shop seen often enough is categorised on its own")
	byCounterparty := map[string]string{}
	for _, tx := range auto.Transactions {
		assert.Equal(t, "model", tx.Categories[0].Source)
		byCounterparty[tx.Counterparty] = tx.Categories[0].CategoryID
	}
	assert.Equal(t, c.categoryID("groceries"), byCounterparty["ALBERT BRNO"])

	var confirmed struct {
		Confirmed int `json:"confirmed"`
	}
	c.do(http.MethodPost, "/api/v1/transactions/"+auto.Transactions[0].ID+"/confirm", nil, http.StatusOK, &confirmed)
	assert.Equal(t, 1, confirmed.Confirmed)
	c.do(http.MethodPost, "/api/v1/transactions/confirm-automatic", nil, http.StatusOK, &confirmed)
	assert.Equal(t, len(auto.Transactions)-1, confirmed.Confirmed)
	assert.Empty(t, c.transactions("auto=true").Transactions)

	var settings struct {
		Threshold float64 `json:"auto_categorize_threshold"`
		Examples  int     `json:"classifier_examples"`
	}
	c.do(http.MethodGet, "/api/v1/settings", nil, http.StatusOK, &settings)
	assert.Equal(t, 0.5, settings.Threshold)
	assert.Equal(t, len(regularShopping)+len(auto.Transactions), settings.Examples, "confirmed choices are learnt from")
}

func TestRepeatedChoiceSuggestsARule(t *testing.T) {
	c := newClient(t)
	visits := []row{
		{"2026-10-01", "KADERNICTVI EVA  PLATBA KARTOU", "", -50000},
		{"2026-10-08", "KADERNICTVI EVA  PLATBA KARTOU", "", -50000},
		{"2026-10-15", "KADERNICTVI EVA  PLATBA KARTOU", "", -60000},
	}
	c.upload("/api/v1/imports", "a.csv", mbankStatement(t, ownMBankAccount, visits), http.StatusCreated, nil)
	beauty := c.categoryID("health.beauty")

	var result struct {
		SuggestedRule *struct {
			Counterparty string `json:"counterparty"`
			Times        int    `json:"times"`
		} `json:"suggested_rule"`
	}
	for i, tx := range c.transactions("").Transactions {
		c.do(http.MethodPut, "/api/v1/transactions/"+tx.ID+"/categories", map[string]any{
			"lines": []map[string]any{{"category_id": beauty, "amount_minor": map[int]int64{0: -60000, 1: -50000, 2: -50000}[i]}},
		}, http.StatusOK, &result)
		if i < 2 {
			assert.Nil(t, result.SuggestedRule, "choice %d", i+1)
		}
	}
	require.NotNil(t, result.SuggestedRule)
	assert.Equal(t, "KADERNICTVI EVA", result.SuggestedRule.Counterparty)
	assert.Equal(t, 3, result.SuggestedRule.Times)
}

func TestTriageShowsTheTransactionPickedFromTheQueue(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "october.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-10", "OBCHOD JEDNA  PLATBA KARTOU", "Neznámá", -10000},
		{"2026-10-12", "OBCHOD DVA  PLATBA KARTOU", "Neznámá", -20000},
	}), http.StatusCreated, nil)
	queue := c.transactions("uncategorized=true")
	require.Len(t, queue.Transactions, 2)
	older := queue.Transactions[len(queue.Transactions)-1].ID

	var triage struct {
		Transaction struct {
			ID string `json:"id"`
		} `json:"transaction"`
	}
	c.do(http.MethodGet, "/api/v1/triage", nil, http.StatusOK, &triage)
	assert.NotEqual(t, older, triage.Transaction.ID, "the newest comes first")
	c.do(http.MethodGet, "/api/v1/triage?id="+older+"&skip="+older, nil, http.StatusOK, &triage)
	assert.Equal(t, older, triage.Transaction.ID, "a pick from the queue wins over skipping it")
}
