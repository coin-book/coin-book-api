package revolut

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/testutil"
)

func parseFixture(t *testing.T) *statement.Statement {
	t.Helper()
	f, err := os.Open("testdata/statement.csv")
	require.NoError(t, err)
	defer f.Close()
	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	return st
}

func TestDetect(t *testing.T) {
	raw, err := os.ReadFile("testdata/statement.csv")
	require.NoError(t, err)
	assert.Equal(t, statement.Certain, Parser{}.Detect(raw))
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("#Datum operace;#Popis operace")))
}

func TestParseGolden(t *testing.T) {
	testutil.AssertGoldenJSON(t, "testdata/statement.golden.json", parseFixture(t))
}

func TestParseReadsAccountsAndSkipsInvestments(t *testing.T) {
	st := parseFixture(t)
	require.Len(t, st.Accounts, 3)

	czk, eur, fund := st.Accounts[0], st.Accounts[1], st.Accounts[2]
	assert.Equal(t, "revolut:personal:CZK", *czk.Ref.ExternalRef)
	assert.Equal(t, "revolut:personal:EUR", *eur.Ref.ExternalRef)
	assert.Equal(t, "revolut:fund:EUR", *fund.Ref.ExternalRef)

	require.Len(t, czk.Rows, 4)
	assert.Equal(t, int64(500000), czk.Rows[0].AmountMinor)
	assert.Equal(t, int64(1500000), *czk.Rows[0].BalanceAfterMinor)
	assert.Equal(t, "topup", czk.Rows[0].OperationType)
	assert.Equal(t, int64(200020), *czk.DeclaredSum)

	require.Len(t, eur.Rows, 3)
	assert.Equal(t, int64(10000), eur.Rows[0].AmountMinor, "the account-currency column wins over the CZK copy")
	assert.Equal(t, "exchange", eur.Rows[0].OperationType)

	require.Len(t, fund.Rows, 2)
	assert.Nil(t, fund.Rows[0].BalanceAfterMinor)
	assert.Equal(t, int64(1), fund.Rows[0].AmountMinor)
}
