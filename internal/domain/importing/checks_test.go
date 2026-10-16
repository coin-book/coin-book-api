package importing

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"coin-book-api/internal/domain/importing/statement"
)

func balance(v int64) *int64 { return &v }

func TestBalanceChainFindsFirstBreak(t *testing.T) {
	acc := &statement.Account{Rows: []statement.Row{
		{Line: 1, AmountMinor: 100, BalanceAfterMinor: balance(1100)},
		{Line: 2, AmountMinor: -50, BalanceAfterMinor: balance(1050)},
		{Line: 3, AmountMinor: -50, BalanceAfterMinor: balance(990)},
	}}

	checks := runChecks(acc)

	assert.Len(t, checks, 1)
	assert.False(t, checks[0].OK)
	assert.Equal(t, 3, checks[0].Line)
	assert.Equal(t, int64(1000), checks[0].ExpectedMinor)
}

func TestTotalsCheckSplitsIncomeAndExpense(t *testing.T) {
	acc := &statement.Account{
		Totals: &statement.Totals{IncomeMinor: 300, ExpenseMinor: -100},
		Rows:   []statement.Row{{AmountMinor: 300}, {AmountMinor: -100}},
	}

	assert.True(t, allPassed(runChecks(acc)))
}

func TestTransactionIDsDistinguishIdenticalRowsByOccurrence(t *testing.T) {
	ref := "19-123/0800"
	acc := &statement.Account{Ref: statement.AccountRef{AccountNumber: &ref}, Rows: []statement.Row{
		{IdentityKey: "same"}, {IdentityKey: "same"}, {IdentityKey: "other"},
	}}

	ids := transactionIDs(acc)

	assert.NotEqual(t, ids[0], ids[1])
	assert.Equal(t, ids, transactionIDs(acc), "ids are deterministic")
}
