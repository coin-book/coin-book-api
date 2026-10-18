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
	assert.Equal(t, "308", own.ConstantSymbol)
}

func TestNormalizeAccountNumber(t *testing.T) {
	assert.Equal(t, "1234567890/0100", NormalizeAccountNumber("000000", "1234567890", "0100"))
	assert.Equal(t, "19-2000000001/0800", NormalizeAccountNumber("000019", "2000000001", "0800"))
	assert.Equal(t, "19-123/0800", NormalizeAccountNumber("19", "0000000123", "0800"))
}

func TestSymbolsGluedToTheMessage(t *testing.T) {
	var row statement.Row
	splitDescription("NAJEMNE RIJEN, BYT 12 KS:0308 VS:0002026001 SS:0000000042  PRONAJIMATEL S.R.O.  000019 2000000001 0800", &row)

	assert.Equal(t, "2026001", row.VariableSymbol)
	assert.Equal(t, "308", row.ConstantSymbol)
	assert.Equal(t, "42", row.SpecificSymbol)
	assert.Equal(t, "NAJEMNE RIJEN, BYT 12", row.Counterparty)
	assert.Equal(t, "PRONAJIMATEL S.R.O.", row.Message)
	assert.Equal(t, "19-2000000001/0800", row.CounterpartyAccount)
}

func TestSymbolsOnlySegment(t *testing.T) {
	var row statement.Row
	splitDescription("VS:0012345678 SS:0000000007  ODCHOZÍ PLATBA  E.ON", &row)

	assert.Equal(t, "12345678", row.VariableSymbol)
	assert.Equal(t, "7", row.SpecificSymbol)
	assert.Equal(t, "ODCHOZÍ PLATBA", row.OperationType)
	assert.Equal(t, "E.ON", row.Counterparty)
}
