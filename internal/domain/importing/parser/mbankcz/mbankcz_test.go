package mbankcz

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/testutil"
)

func TestDetect(t *testing.T) {
	raw, err := os.ReadFile("testdata/statement.csv")
	require.NoError(t, err)

	assert.Equal(t, statement.Certain, Parser{}.Detect(raw))
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("Dátum,Popis,Kategória")))
}

func TestParseGolden(t *testing.T) {
	f, err := os.Open("testdata/statement.csv")
	require.NoError(t, err)
	defer f.Close()

	st, err := Parser{}.Parse(f)
	require.NoError(t, err)

	testutil.AssertGoldenJSON(t, "testdata/statement.golden.json", st)
}

func TestParseSplitsDescription(t *testing.T) {
	f, err := os.Open("testdata/statement.csv")
	require.NoError(t, err)
	defer f.Close()

	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	require.Len(t, st.Accounts, 1)
	acc := st.Accounts[0]

	assert.Equal(t, "19-1234567890/6210", *acc.Ref.AccountNumber)
	assert.Equal(t, int64(6200000), acc.Totals.IncomeMinor)

	card := acc.Rows[0]
	assert.Equal(t, "PLATBA KARTOU", card.OperationType)
	assert.Equal(t, "ROHLIK.CZ", card.Counterparty)
	assert.Equal(t, int64(-124900), card.AmountMinor)

	salary := acc.Rows[3]
	assert.Equal(t, "2026001", salary.VariableSymbol)
	assert.Equal(t, "FIRMA TEST S.R.O.", salary.Counterparty)
	assert.Equal(t, "VYPLATA 09/2026", salary.Message)
	assert.Equal(t, "19-2000000001/0800", salary.CounterpartyAccount)

	own := acc.Rows[4]
	assert.Equal(t, "VLASTNÍ PŘEVOD", own.OperationType)
	assert.Equal(t, "9999999999/2010", own.CounterpartyAccount)
	assert.Equal(t, "0308", own.ConstantSymbol)
}

func TestNormalizeAccountNumber(t *testing.T) {
	assert.Equal(t, "1234567890/0100", NormalizeAccountNumber("000000", "1234567890", "0100"))
	assert.Equal(t, "19-2000000001/0800", NormalizeAccountNumber("000019", "2000000001", "0800"))
	assert.Equal(t, "19-123/0800", NormalizeAccountNumber("19", "0000000123", "0800"))
}
