package generic

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/testutil"
)

func bankExport(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/bank-export.csv")
	require.NoError(t, err)
	return raw
}

func TestInspectGuessesHeaderAndColumnsUnderABankPreamble(t *testing.T) {
	inspection, err := Inspect(bankExport(t), "", "")
	require.NoError(t, err)
	layout := inspection.Suggested

	assert.Equal(t, UTF8, layout.Encoding)
	assert.Equal(t, ";", layout.Delimiter)
	assert.Equal(t, 4, layout.HeaderRow)
	assert.Equal(t, map[Field]int{FieldDate: 0, FieldCounterparty: 1, FieldCounterpartyAccount: 2, FieldBankCode: 3,
		FieldMessage: 4, FieldVariableSymbol: 5, FieldCredit: 6, FieldDebit: 7, FieldBalanceAfter: 8}, layout.Columns)
	assert.Equal(t, DayMonthYear, layout.DateOrder)
	assert.Equal(t, "CZK", string(layout.Currency))
	assert.NoError(t, layout.Validate())
}

func TestParseLayoutGolden(t *testing.T) {
	inspection, err := Inspect(bankExport(t), "", "")
	require.NoError(t, err)

	st, err := ParseLayout(bankExport(t), inspection.Suggested)
	require.NoError(t, err)
	testutil.AssertGoldenJSON(t, "testdata/bank-export.golden.json", st)
}

func TestParseLayoutReadsCreditDebitAndBankCode(t *testing.T) {
	inspection, err := Inspect(bankExport(t), "", "")
	require.NoError(t, err)
	st, err := ParseLayout(bankExport(t), inspection.Suggested)
	require.NoError(t, err)

	require.Len(t, st.Accounts, 1)
	rows := st.Accounts[0].Rows
	require.Len(t, rows, 3, "preamble, blank row and the totals footer are skipped")
	assert.Equal(t, int64(-124950), rows[0].AmountMinor, "a positive debit is money out")
	assert.Equal(t, int64(6200000), rows[1].AmountMinor)
	assert.Equal(t, "2000000001/0800", rows[1].CounterpartyAccount)
	assert.Equal(t, "2026001", rows[1].VariableSymbol)
	assert.Equal(t, int64(-8900), rows[2].AmountMinor, "a negative debit is money out too")
	assert.Equal(t, "2026-10-05", rows[2].BookedOn.Format("2006-01-02"), "the time is ignored")
	assert.Equal(t, int64(7066150), *rows[2].BalanceAfterMinor)
	assert.Contains(t, *st.Accounts[0].Ref.ExternalRef, "mapped:")
}

func TestFingerprintFollowsTheColumnNamesNotTheRows(t *testing.T) {
	inspection, err := Inspect(bankExport(t), "", "")
	require.NoError(t, err)
	layout := inspection.Suggested
	other := []byte("x;;;;;;;;\nx;;;;;;;;\nx;;;;;;;;\n;;;;;;;;\n" +
		"Datum zaúčtování;Název protiúčtu;Protiúčet;Kód banky;Zpráva pro příjemce;VS;Příjem;Výdaj;Zůstatek\n" +
		"01.11.2026;A;;;;;;1,00;0,00\n")

	assert.Equal(t, layout.Fingerprint(bankExport(t)), layout.Fingerprint(other))
	assert.NotEqual(t, layout.Fingerprint(bankExport(t)), layout.Fingerprint([]byte(genericHeaderOnly)))
}

const genericHeaderOnly = "datum;castka;mena;popis\n2026-10-01;1;CZK;a\n"

func TestRowInsideTheDataThatDoesNotFitIsAnError(t *testing.T) {
	layout := Layout{Encoding: UTF8, Delimiter: ";", HeaderRow: 0, DateOrder: DayMonthYear, Currency: "CZK",
		Columns: map[Field]int{FieldDate: 0, FieldAmount: 1, FieldDescription: 2}}
	raw := []byte("Datum;Částka;Popis\n01.10.2026;-1,00;a\n02.10.2026;nic;b\n03.10.2026;-2,00;c\n")

	_, err := ParseLayout(raw, layout)
	assert.ErrorIs(t, err, ErrLayoutMismatch)
}

func TestLayoutValidation(t *testing.T) {
	valid := Layout{Encoding: UTF8, Delimiter: ";", DateOrder: DayMonthYear, Currency: "CZK",
		Columns: map[Field]int{FieldDate: 0, FieldAmount: 1, FieldDescription: 2}}
	require.NoError(t, valid.Validate())

	both := valid
	both.Columns = map[Field]int{FieldDate: 0, FieldAmount: 1, FieldCredit: 3, FieldDescription: 2}
	assert.ErrorIs(t, both.Validate(), ErrInvalidLayout)

	shared := valid
	shared.Columns = map[Field]int{FieldDate: 0, FieldAmount: 0, FieldDescription: 2}
	assert.ErrorIs(t, shared.Validate(), ErrInvalidLayout)

	noCurrency := valid
	noCurrency.Currency = ""
	assert.ErrorIs(t, noCurrency.Validate(), ErrInvalidLayout)
}

func TestDatesInEveryOrder(t *testing.T) {
	for raw, order := range map[string]DateOrder{"5.10.2026": DayMonthYear, "10/05/2026": MonthDayYear,
		"2026-10-05": DayMonthYear, "05.10.26": DayMonthYear, "2026/10/05 08:00": YearMonthDay} {
		got, err := parseOrderedDate(raw, order)
		require.NoError(t, err, raw)
		assert.Equal(t, "2026-10-05", got.Format("2006-01-02"), raw)
	}
	_, err := parseOrderedDate("31.02.2026", DayMonthYear)
	assert.Error(t, err)
}
