//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var octoberRows = []row{
	{"2026-10-02", "ROHLIK.CZ  PLATBA KARTOU", "Potraviny a drogerie", -124900},
	{"2026-10-05", "KAVARNA U TESTU  PLATBA KARTOU", "Restaurace", -8900},
	{"2026-10-10", "VS:2026001  FIRMA TEST S.R.O.  VYPLATA  000019 2000000001 0800", "Výplata", 6200000},
	{"2026-10-12", "ODCHOZÍ OKAMŽITÁ PLATBA  DOBITI REVOLUT", "Bez kategorie - jiné", -500000},
	{"2026-10-14", "OBCHOD BEZ KATEGORIE  PLATBA KARTOU", "Neznámá", -30000},
}

// AK 1: both formats import cleanly and the control totals agree.
func TestAK1ImportsBothFormats(t *testing.T) {
	c := newClient(t)

	var mbank, revolut importResult
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, &mbank)
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(100000, []row{{"2026-10-13", "Dobitie", "Dobiť účet", 500000}}), http.StatusCreated, &revolut)

	assert.True(t, mbank.ChecksPassed)
	assert.Equal(t, len(octoberRows), mbank.RowsNew)
	assert.True(t, revolut.ChecksPassed)
	assert.Equal(t, 1, revolut.RowsNew)
}

func TestAK1RefusesStatementThatDisagreesWithItsTotals(t *testing.T) {
	c := newClient(t)
	content := mbankStatement(t, ownMBankAccount, octoberRows)
	tampered := bytes.Replace(content, []byte("-89,00 CZK"), []byte("-98,00 CZK"), 1)

	c.upload("/api/v1/imports", "mbank.csv", tampered, http.StatusConflict, nil)
	assert.Empty(t, c.transactions("").Transactions)

	var forced importResult
	c.upload("/api/v1/imports?force=true", "mbank.csv", tampered, http.StatusCreated, &forced)
	assert.False(t, forced.ChecksPassed)
	assert.Len(t, c.transactions("").Transactions, len(octoberRows))
}

// AK 2: the same file adds nothing; an overlapping export adds only the new rows.
func TestAK2ImportIsIdempotent(t *testing.T) {
	c := newClient(t)
	first := mbankStatement(t, ownMBankAccount, octoberRows[:3])
	c.upload("/api/v1/imports", "a.csv", first, http.StatusCreated, nil)

	c.upload("/api/v1/imports", "a.csv", first, http.StatusConflict, nil)

	var overlap importResult
	c.upload("/api/v1/imports", "b.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, &overlap)
	assert.Equal(t, 2, overlap.RowsNew)
	assert.Equal(t, 3, overlap.RowsDuplicate)
	assert.Len(t, c.transactions("").Transactions, len(octoberRows))
}

func TestAK2IdenticalRowsOnOneDayAreKept(t *testing.T) {
	c := newClient(t)
	twice := []row{octoberRows[0], octoberRows[0]}

	var result importResult
	c.upload("/api/v1/imports", "a.csv", mbankStatement(t, ownMBankAccount, twice), http.StatusCreated, &result)

	assert.Equal(t, 2, result.RowsNew)
}

// AK 3: a dry run writes nothing; a rollback removes the whole import.
func TestAK3DryRunAndRollback(t *testing.T) {
	c := newClient(t)
	content := mbankStatement(t, ownMBankAccount, octoberRows)

	var preview importResult
	c.upload("/api/v1/imports/preview", "a.csv", content, http.StatusOK, &preview)
	assert.Equal(t, len(octoberRows), preview.RowsNew)
	assert.Empty(t, preview.BatchID)
	assert.Empty(t, c.transactions("").Transactions)

	var committed importResult
	c.upload("/api/v1/imports", "a.csv", content, http.StatusCreated, &committed)
	var rollback struct {
		Removed int `json:"removed"`
	}
	c.do(http.MethodPost, "/api/v1/imports/"+committed.BatchID+"/rollback", nil, http.StatusOK, &rollback)

	assert.Equal(t, len(octoberRows), rollback.Removed)
	assert.Empty(t, c.transactions("").Transactions)
	c.upload("/api/v1/imports", "a.csv", content, http.StatusCreated, nil)
}

// AK 4: an mBank → Revolut top-up within three days is one transfer and counts as neither income nor expense.
func TestAK4TransferIsPairedAndExcluded(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)

	var revolut importResult
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(0, []row{{"2026-10-14", "Dobitie", "Dobiť účet", 500000}}), http.StatusCreated, &revolut)
	assert.Equal(t, 1, revolut.TransfersMatched)

	var report struct {
		IncomeMinor  int64 `json:"income_minor"`
		ExpenseMinor int64 `json:"expense_minor"`
	}
	c.do(http.MethodGet, "/api/v1/reports/monthly/2026-10", nil, http.StatusOK, &report)
	assert.Equal(t, int64(6200000), report.IncomeMinor)
	assert.Equal(t, int64(124900+8900+30000), report.ExpenseMinor)
}

func TestAK4AmbiguousTransferWaitsForTheUser(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	c.upload("/api/v1/imports", "revolut.csv", revolutStatement(0, []row{
		{"2026-10-11", "Dobitie", "Dobiť účet", 500000},
		{"2026-10-13", "Dobitie", "Dobiť účet", 500000},
	}), http.StatusCreated, nil)

	var ambiguous struct {
		Ambiguous []struct {
			TransactionID string   `json:"transaction_id"`
			CandidateIDs  []string `json:"candidate_ids"`
		} `json:"ambiguous"`
	}
	c.do(http.MethodGet, "/api/v1/transfers/ambiguous", nil, http.StatusOK, &ambiguous)
	require.Len(t, ambiguous.Ambiguous, 1)
	require.Len(t, ambiguous.Ambiguous[0].CandidateIDs, 2)

	pick := ambiguous.Ambiguous[0]
	c.do(http.MethodPost, "/api/v1/transfers", map[string]string{"out_tx_id": pick.TransactionID, "in_tx_id": pick.CandidateIDs[0]}, http.StatusCreated, nil)
	c.do(http.MethodGet, "/api/v1/transfers/ambiguous", nil, http.StatusOK, &ambiguous)
	assert.Empty(t, ambiguous.Ambiguous)
}

// AK 5: everything is categorised or queued; a manual choice saved as a rule applies to the next import.
func TestAK5QueueAndRuleFromManualChoice(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)

	all := c.transactions("")
	queued := c.transactions("uncategorized=true")
	for _, tx := range all.Transactions {
		inQueue := false
		for _, q := range queued.Transactions {
			inQueue = inQueue || q.ID == tx.ID
		}
		assert.True(t, len(tx.Categories) > 0 || inQueue, tx.ID)
	}
	require.NotEmpty(t, queued.Transactions)

	var triage struct {
		Transaction struct {
			ID           string `json:"id"`
			Counterparty string `json:"counterparty"`
		} `json:"transaction"`
		Remaining int `json:"remaining"`
	}
	c.do(http.MethodGet, "/api/v1/triage", nil, http.StatusOK, &triage)
	require.Equal(t, "OBCHOD BEZ KATEGORIE", triage.Transaction.Counterparty)

	shopping := c.categoryID("shopping")
	c.do(http.MethodPut, "/api/v1/transactions/"+triage.Transaction.ID+"/categories", map[string]any{
		"lines":     []map[string]any{{"category_id": shopping, "amount_minor": -30000}},
		"save_rule": "counterparty",
	}, http.StatusOK, nil)

	var next importResult
	c.upload("/api/v1/imports", "november.csv", mbankStatement(t, ownMBankAccount, []row{
		{"2026-10-15", "OBCHOD BEZ KATEGORIE  PLATBA KARTOU", "Neznámá", -45000},
	}), http.StatusCreated, &next)
	assert.Equal(t, 1, next.CategorizedRule)
}

func TestAK5SplitMustAddUp(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows[:1]), http.StatusCreated, nil)
	id := c.transactions("").Transactions[0].ID
	groceries, home := c.categoryID("groceries"), c.categoryID("shopping.home")

	c.do(http.MethodPut, "/api/v1/transactions/"+id+"/categories", map[string]any{"lines": []map[string]any{
		{"category_id": groceries, "amount_minor": -100000}, {"category_id": home, "amount_minor": -10000},
	}}, http.StatusUnprocessableEntity, nil)
	c.do(http.MethodPut, "/api/v1/transactions/"+id+"/categories", map[string]any{"lines": []map[string]any{
		{"category_id": groceries, "amount_minor": -100000}, {"category_id": home, "amount_minor": -24900},
	}}, http.StatusOK, nil)
}

// AK 6: the month budget shows plan, actual and remainder, and warns ahead of the limit.
func TestAK6BudgetShowsPaceAndOverspend(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	groceries, dining := c.categoryID("groceries"), c.categoryID("dining")

	var budget struct {
		Lines []struct {
			CategoryID     string `json:"category_id"`
			PlannedMinor   int64  `json:"planned_minor"`
			SpentMinor     int64  `json:"spent_minor"`
			RemainingMinor int64  `json:"remaining_minor"`
			Status         string `json:"status"`
		} `json:"lines"`
		DaysLeft int `json:"days_left"`
	}
	c.do(http.MethodPut, "/api/v1/budgets/2026-10", map[string]any{"lines": []map[string]any{
		{"category_id": groceries, "planned_minor": 200000},
		{"category_id": dining, "planned_minor": 5000},
	}}, http.StatusOK, &budget)

	require.Len(t, budget.Lines, 2)
	byCategory := map[string]int{}
	for i, l := range budget.Lines {
		byCategory[l.CategoryID] = i
	}
	g, d := budget.Lines[byCategory[groceries]], budget.Lines[byCategory[dining]]
	assert.Equal(t, int64(124900), g.SpentMinor)
	assert.Equal(t, int64(75100), g.RemainingMinor)
	assert.Equal(t, "ahead_of_pace", g.Status, "1 249 Kč by the 16th of 31 days projects past 2 000 Kč")
	assert.Equal(t, "over", d.Status)
	assert.Equal(t, 16, budget.DaysLeft)
}

// AK 7: a read-only agent token reads, cannot write, and every access is audited.
func TestAK7AgentTokenIsReadOnlyAndAudited(t *testing.T) {
	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)

	var created struct {
		Secret string `json:"secret"`
	}
	c.do(http.MethodPost, "/api/v1/agent-tokens", map[string]any{"name": "asistent", "scopes": []string{"transactions:read", "budgets:read"}}, http.StatusCreated, &created)
	require.True(t, strings.HasPrefix(created.Secret, "coinbook_pat_"))
	agent := c.withToken(created.Secret)

	assert.Len(t, agent.transactions("").Transactions, len(octoberRows))
	agent.do(http.MethodGet, "/api/v1/budgets/2026-10", nil, http.StatusOK, nil)
	agent.do(http.MethodGet, "/api/v1/accounts", nil, http.StatusForbidden, nil)
	agent.do(http.MethodPut, "/api/v1/budgets/2026-10", map[string]any{"lines": []any{}}, http.StatusForbidden, nil)
	agent.do(http.MethodGet, "/api/v1/agent-tokens", nil, http.StatusForbidden, nil)

	var audit struct {
		Entries []struct {
			Operation   string `json:"operation"`
			Status      int    `json:"status"`
			ResultCount *int   `json:"result_count"`
		} `json:"entries"`
	}
	c.do(http.MethodGet, "/api/v1/audit-log", nil, http.StatusOK, &audit)
	require.Len(t, audit.Entries, 5)
	last := audit.Entries[len(audit.Entries)-1]
	assert.Equal(t, "list-transactions", last.Operation)
	assert.Equal(t, http.StatusOK, last.Status)
	require.NotNil(t, last.ResultCount)
	assert.Equal(t, len(octoberRows), *last.ResultCount)
	assert.Equal(t, http.StatusForbidden, audit.Entries[0].Status)
}

// AK 8: 10 000 rows import in under two seconds.
func TestAK8TenThousandRowsUnderTwoSeconds(t *testing.T) {
	c := newClient(t)
	rows := make([]row, 10000)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range rows {
		day := start.AddDate(0, 0, i/20).Format(time.DateOnly)
		rows[i] = row{day, fmt.Sprintf("OBCHOD %d  PLATBA KARTOU", i%300), "Potraviny a drogerie", -int64(1000 + i)}
	}
	content := mbankStatement(t, ownMBankAccount, rows)

	began := time.Now()
	var result importResult
	c.upload("/api/v1/imports", "big.csv", content, http.StatusCreated, &result)
	elapsed := time.Since(began)

	assert.Equal(t, 10000, result.RowsNew)
	assert.Less(t, elapsed, 2*time.Second)
	t.Logf("10 000 rows imported in %s", elapsed)
}

// AK 9: logs never carry a full account number.
func TestAK9LogsDoNotContainAccountNumbers(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	c := newClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	c.upload("/api/v1/imports", "mbank.csv", []byte("not a statement"), http.StatusUnprocessableEntity, nil)

	assert.NotContains(t, logs.String(), "1234567890")
	assert.NotContains(t, logs.String(), "2000000001")
}

func TestRealExportsImportCleanly(t *testing.T) {
	c := newClient(t)
	for _, env := range []string{"COINBOOK_REAL_MBANK_CZ", "COINBOOK_REAL_REVOLUT"} {
		path := os.Getenv(env)
		if path == "" {
			continue
		}
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		var result importResult
		c.upload("/api/v1/imports", "real.csv", content, http.StatusCreated, &result)
		t.Logf("%s rows=%d new=%d rule=%d bank=%d transfers=%d checks=%v", env, result.RowsTotal, result.RowsNew,
			result.CategorizedRule, result.CategorizedBank, result.TransfersMatched, result.ChecksPassed)
		assert.True(t, result.ChecksPassed)
	}
}
