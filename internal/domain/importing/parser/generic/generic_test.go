package generic

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/testutil"
)

func parseFile(t *testing.T, path string) *statement.Statement {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	return st
}

func TestDetectByHeaderInAnyLanguageAndDelimiter(t *testing.T) {
	assert.Equal(t, statement.Likely, Parser{}.Detect([]byte("\xef\xbb\xbfDatum;Částka;Měna;Popis\n")))
	assert.Equal(t, statement.Likely, Parser{}.Detect([]byte("description,currency,amount,date\n")))
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("date,amount,description\n")), "currency is required")
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("#Datum operace;#Popis operace;#Účet;#Kategorie;#Částka;")))
}

func TestParseGolden(t *testing.T) {
	testutil.AssertGoldenJSON(t, "testdata/statement.golden.json", parseFile(t, "testdata/statement.csv"))
}

func TestSingleAccountFileNamesNoAccount(t *testing.T) {
	st := parseFile(t, "testdata/statement.csv")
	require.Len(t, st.Accounts, 1)
	acc := st.Accounts[0]

	assert.Empty(t, acc.Ref.Identity(), "the user maps it on import")
	assert.Equal(t, int64(-124950), acc.Rows[0].AmountMinor)
	assert.Equal(t, int64(6200000), acc.Rows[1].AmountMinor)
	assert.Equal(t, "Potraviny", acc.Rows[0].CategoryHint)
	assert.Equal(t, int64(1000050), *acc.Rows[0].BalanceAfterMinor)
	assert.Equal(t, "Kava", acc.Rows[2].Description)
	assert.Equal(t, "2026-10-15", st.PeriodFrom.Format("2006-01-02"))
}

func TestAccountColumnSplitsAccountsAndIDsKeyRows(t *testing.T) {
	st := parseFile(t, "testdata/multi-account.csv")
	require.Len(t, st.Accounts, 2)

	wise, cash := st.Accounts[0], st.Accounts[1]
	assert.Equal(t, "csv:Wise EUR", wise.Ref.Identity())
	assert.Equal(t, "EUR", string(wise.Ref.Currency))
	assert.Equal(t, int64(-1250), wise.Rows[0].AmountMinor, "dot decimal")
	assert.Equal(t, int64(125000), wise.Rows[1].AmountMinor)
	assert.Equal(t, "id:w-1", wise.Rows[0].IdentityKey)
	assert.Equal(t, "Hotovost", cash.Ref.Name)
}

func TestRefusesTwoCurrenciesInOneAccount(t *testing.T) {
	_, err := Parser{}.Parse(strings.NewReader("date,amount,currency,description\n2026-10-01,1,CZK,a\n2026-10-01,1,EUR,b\n"))
	assert.ErrorIs(t, err, ErrMixedCurrency)
}

func TestReportsMissingColumn(t *testing.T) {
	_, err := Parser{}.Parse(strings.NewReader("date,amount,description\n2026-10-01,1,a\n"))
	assert.ErrorIs(t, err, ErrMissingColumn)
}
