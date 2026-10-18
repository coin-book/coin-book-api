package airbank

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/testutil"
)

const utf8Export = "testdata/airbank_1234567890_2026-10-18_12-00.csv"

func parse(t *testing.T, path string) *statement.Statement {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	st, err := Parser{}.Parse(bytes.NewReader(raw))
	require.NoError(t, err)
	return st
}

func TestDetectBothDelimitersAndEncodings(t *testing.T) {
	for _, path := range []string{utf8Export, "testdata/semicolon-windows-1250.csv"} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, statement.Certain, Parser{}.Detect(raw), path)
	}
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("datum;castka;mena;popis\n")))
}

func TestParseGolden(t *testing.T) {
	testutil.AssertGoldenJSON(t, "testdata/statement.golden.json", parse(t, utf8Export))
}

func TestRows(t *testing.T) {
	st := parse(t, utf8Export)
	require.Len(t, st.Accounts, 1)
	rows := st.Accounts[0].Rows
	require.Len(t, rows, 3, "a card payment not yet posted waits for a later export")

	card := rows[0]
	assert.Equal(t, "2026-10-02", card.BookedOn.Format("2006-01-02"), "the day of the payment, not of its posting")
	assert.Equal(t, int64(-124950), card.AmountMinor)
	assert.Equal(t, "ROHLIK.CZ", card.Description)
	assert.Equal(t, "Platba kartou", card.OperationType)
	assert.Equal(t, "ref:100000001", card.IdentityKey)

	salary := rows[1]
	assert.Equal(t, "2000000001/0800", salary.CounterpartyAccount)
	assert.Equal(t, "2026001", salary.VariableSymbol)
	assert.Equal(t, "Výplata 09/2026", salary.Message)

	payment := rows[2]
	assert.Equal(t, int64(-51000), payment.AmountMinor, "the fee leaves the account too")
	assert.Equal(t, int64(1000), payment.FeeMinor)
	assert.Equal(t, "308", payment.ConstantSymbol)
}

func TestOlderSemicolonExportInWindows1250(t *testing.T) {
	st := parse(t, "testdata/semicolon-windows-1250.csv")
	rows := st.Accounts[0].Rows
	require.Len(t, rows, 3)
	assert.Equal(t, "Nákup potravin", rows[0].Message)
	assert.Contains(t, rows[0].IdentityKey, "2026-10-02|-124950|ROHLIK.CZ", "without a reference number the row keys on its content")
}

func TestAccountNumberFromFileName(t *testing.T) {
	assert.Equal(t, "1234567890/3030", Parser{}.AccountNumberFromFileName("airbank_1234567890_2026-10-18_12-00.csv"))
	assert.Equal(t, "19-1234567890/3030", Parser{}.AccountNumberFromFileName("AirBank_19-1234567890_2026.csv"))
	assert.Empty(t, Parser{}.AccountNumberFromFileName("vypis.csv"))
}
