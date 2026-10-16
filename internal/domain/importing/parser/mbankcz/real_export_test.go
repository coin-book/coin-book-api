package mbankcz

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Runs against a real export kept outside the repository; prints counts only.
func TestRealExport(t *testing.T) {
	path := os.Getenv("COINBOOK_REAL_MBANK_CZ")
	if path == "" {
		t.Skip("set COINBOOK_REAL_MBANK_CZ to a real mBank CZ export")
	}
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	acc := st.Accounts[0]

	var income, expense int64
	withOperation, withCounterpartyAccount := 0, 0
	for _, r := range acc.Rows {
		if r.AmountMinor > 0 {
			income += r.AmountMinor
		} else {
			expense += r.AmountMinor
		}
		if r.OperationType != "" {
			withOperation++
		}
		if r.CounterpartyAccount != "" {
			withCounterpartyAccount++
		}
	}
	t.Logf("rows=%d with_operation=%d with_counterparty_account=%d", len(acc.Rows), withOperation, withCounterpartyAccount)
	require.NotNil(t, acc.Totals)
	assert.Equal(t, acc.Totals.IncomeMinor, income, "income total")
	assert.Equal(t, acc.Totals.ExpenseMinor, expense, "expense total")
}
