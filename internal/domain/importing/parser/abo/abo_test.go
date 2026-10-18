package abo

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/testutil"
)

func parseFile(t *testing.T) *statement.Statement {
	t.Helper()
	f, err := os.Open("testdata/statement.gpc")
	require.NoError(t, err)
	defer f.Close()
	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	return st
}

func TestDetect(t *testing.T) {
	raw, err := os.ReadFile("testdata/statement.gpc")
	require.NoError(t, err)

	assert.Equal(t, statement.Certain, Parser{}.Detect(raw))
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("date,amount,currency,description\n")))
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("075000019000012345")))
}

func TestParseGolden(t *testing.T) {
	testutil.AssertGoldenJSON(t, "testdata/statement.golden.json", parseFile(t))
}

func TestStatementsOfOneAccountMergeAndCarryTheirBalances(t *testing.T) {
	st := parseFile(t)
	require.Len(t, st.Accounts, 1)
	acc := st.Accounts[0]

	assert.Equal(t, "19-123457", acc.Ref.Identity())
	assert.Equal(t, "Běžný účet TEST", acc.Ref.Name)
	assert.Equal(t, "CZK", string(acc.Ref.Currency))
	assert.Equal(t, int64(1500000), *acc.OpeningBalanceMinor)
	assert.Equal(t, int64(6200000-124950-35000+12000-24900), *acc.DeclaredSum)
	assert.Len(t, acc.Rows, 5)
	assert.Equal(t, "2026-10-01", st.PeriodFrom.Format("2006-01-02"))
	assert.Equal(t, "2026-10-04", st.PeriodTo.Format("2006-01-02"))
}

func TestTransactionFields(t *testing.T) {
	rows := parseFile(t).Accounts[0].Rows

	electricity := rows[0]
	assert.Equal(t, int64(-124950), electricity.AmountMinor)
	assert.Equal(t, "987654321/0800", electricity.CounterpartyAccount)
	assert.Equal(t, "1234567890", electricity.VariableSymbol)
	assert.Equal(t, "308", electricity.ConstantSymbol)
	assert.Equal(t, "Elektrárna Test", electricity.Description)
	assert.Equal(t, "Záloha za říjen zákaznické číslo 42", electricity.Message)

	assert.Equal(t, "1111222233/2010", rows[1].CounterpartyAccount)
	assert.Equal(t, "Výběr z bankomatu", rows[2].Description, "without a counterparty the message names the row")
	assert.Equal(t, int64(12000), rows[3].AmountMinor, "a reversed debit returns money")
	assert.Equal(t, "STORNO", rows[3].OperationType)
	assert.Equal(t, "2026-10-04", rows[4].BookedOn.Format("2006-01-02"), "the stripped last record still parses")
}

func TestRefusesTransactionBeforeHeader(t *testing.T) {
	_, err := Parser{}.Parse(strings.NewReader("075" + strings.Repeat("0", 125) + "\n"))
	assert.ErrorIs(t, err, ErrNoStatementHeader)
}

func TestOperationCodesInTheCurrencyColumnLeaveTheAccountInCZK(t *testing.T) {
	raw, err := os.ReadFile("testdata/statement.gpc")
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\r\n")
	for i, code := range map[int]string{1: "0114", 3: "0306", 4: "0840"} {
		lines[i] = lines[i][:118] + code + lines[i][122:]
	}

	st, err := Parser{}.Parse(strings.NewReader(strings.Join(lines, "\r\n")))
	require.NoError(t, err)
	assert.Equal(t, "CZK", string(st.Accounts[0].Ref.Currency))
}

func TestOneISOCodeOnEveryRowSetsTheCurrency(t *testing.T) {
	raw, err := os.ReadFile("testdata/statement.gpc")
	require.NoError(t, err)

	st, err := Parser{}.Parse(strings.NewReader(strings.ReplaceAll(string(raw), "0203", "0978")))
	require.NoError(t, err)
	assert.Equal(t, "EUR", string(st.Accounts[0].Ref.Currency))
}
