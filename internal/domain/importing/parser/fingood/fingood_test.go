package fingood

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/xlsx"
	"coin-book-api/internal/testutil"
)

var header = []any{"Číslo úvěru", "Název investice", "Způsob splácení", "Původní jistina", "Nakoupeno jistiny na Marketu",
	"Prodáno jistiny na Marketu", "Prodáno na Marketu za", "Nakoupeno na Marketu za", "Přislíbeno", "Úrok % (p.a.)",
	"Termín další splátky", "Termín poslední splátky", "Příští splátka", "Stav", "Stav splácení", "Investováno dne",
	"Zpoždění (dnů)", "Po splatnosti", "Sankční úroky", "Vráceno úroků", "Vráceno jistiny", "Poplatky",
	"Poplatky z prodeje na Marketu"}

type n = xlsx.Number

// exportedAt is 23:30 in Prague on 18 October, already the 18th in UTC too.
var exportedAt = time.Date(2026, 10, 18, 21, 30, 0, 0, time.UTC)

// synthetic copies the real export's shape (importer-formaty.md): a balloon loan being repaid,
// an annuity loan late with a penalty, one bought and partly sold on the market, one in
// preparation and one repaid. Names and amounts are made up.
func synthetic(t *testing.T) []byte {
	t.Helper()
	content, err := xlsx.Build([][]any{
		header,
		{"1000000001", "Rekonstrukce bytového domu - 1. etapa", "Balonově", n("50000"), n("0"), n("0"), n("0"), n("0"), n("0"), n("9.5"),
			n("46325"), n("46660"), n("395.83"), "Splácení", "V pořádku", n("45930"), nil, n("0"), n("0"), n("1187.49"), n("0"), n("0"), n("0")},
		{"1000000002", "Výstavba haly", "Anuitně", n("20000.000000000004"), n("0"), n("0"), n("0"), n("0"), n("0"), n("11"),
			n("46301"), n("46900"), n("650.5"), "Splácení", "Ve zpoždění", n("45600"), n("17"), n("650.5"), n("12.3"), n("2100.75"), n("4800"), n("15"), n("0")},
		{"1000000003", "Nákup pozemku", "Balonově", n("10000"), n("5000"), n("2000"), n("2050"), n("5100"), n("0"), n("10"),
			n("46320"), n("46500"), n("125"), "Splácení", "V pořádku", n("45800"), nil, n("0"), n("0"), n("410"), n("0"), n("0"), n("20.5")},
		{"1000000004", "Developerský projekt", "Balonově", n("30000"), n("0"), n("0"), n("0"), n("0"), n("0"), n("10.5"),
			nil, nil, nil, "Příprava smluv", nil, n("46310"), nil, nil, nil, nil, nil, nil, n("0")},
		{"1000000005", "Splacený úvěr", "Balonově", n("15000"), n("0"), n("0"), n("0"), n("0"), n("0"), n("9"),
			nil, nil, nil, "Splacen", nil, n("45500"), nil, n("0"), n("0"), n("2025"), n("15000"), n("0"), n("0")},
	}, exportedAt)
	require.NoError(t, err)
	return content
}

func parse(t *testing.T, content []byte) *statement.Statement {
	t.Helper()
	st, err := Parser{}.Parse(bytes.NewReader(content))
	require.NoError(t, err)
	return st
}

func TestDetectsAWorkbook(t *testing.T) {
	assert.Equal(t, statement.Likely, Parser{}.Detect(synthetic(t)))
	assert.Equal(t, statement.NoMatch, Parser{}.Detect([]byte("Číslo úvěru;Název investice\n")))
}

func TestParseGolden(t *testing.T) {
	testutil.AssertGoldenJSON(t, "testdata/statement.golden.json", parse(t, synthetic(t)))
}

func TestOutstandingPrincipal(t *testing.T) {
	st := parse(t, synthetic(t))
	require.Len(t, st.Accounts, 1)
	acc := st.Accounts[0]
	assert.Equal(t, ExternalRef, *acc.Ref.ExternalRef)
	assert.Equal(t, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), acc.P2P.TakenOn)
	byLoan := map[string]statement.P2PPosition{}
	for _, p := range acc.P2P.Positions {
		byLoan[p.LoanNumber] = p
	}
	assert.Equal(t, int64(50_000_00), byLoan["1000000001"].OutstandingMinor)
	assert.Equal(t, int64(15_200_00), byLoan["1000000002"].OutstandingMinor, "less the principal returned")
	assert.Equal(t, int64(13_000_00), byLoan["1000000003"].OutstandingMinor, "plus bought, less sold on the market")
	assert.Equal(t, int64(-50_00), byLoan["1000000003"].MarketGainMinor, "sold 50 above principal, bought 100 above")
	assert.Equal(t, int64(20_50), byLoan["1000000003"].FeesMinor)
	assert.Equal(t, int64(30_000_00), byLoan["1000000004"].OutstandingMinor, "money committed to a loan in preparation")
	assert.Zero(t, byLoan["1000000005"].OutstandingMinor)
	late := byLoan["1000000002"]
	assert.Equal(t, 17, late.DelayDays)
	assert.Equal(t, "Ve zpoždění", late.RepaymentStatus)
	assert.True(t, decimal.RequireFromString("11").Equal(late.InterestRate))
	require.NotNil(t, late.FinalPaymentOn)
	assert.Equal(t, "2028-05-27", late.FinalPaymentOn.Format(time.DateOnly))
}

func TestRefusesAnExportWithoutItsColumns(t *testing.T) {
	content, err := xlsx.Build([][]any{{"Datum", "Částka"}, {n("46000"), n("1")}}, exportedAt)
	require.NoError(t, err)
	_, err = Parser{}.Parse(bytes.NewReader(content))
	assert.ErrorIs(t, err, ErrMissingColumn)
}

func TestRefusesAnExportWithoutADate(t *testing.T) {
	content, err := xlsx.Build([][]any{header, {"1000000001", "X", "Balonově", n("1")}}, time.Time{})
	require.NoError(t, err)
	_, err = Parser{}.Parse(bytes.NewReader(content))
	assert.ErrorIs(t, err, ErrNoExportDate)
}

func TestRefusesAnInvalidAmount(t *testing.T) {
	content, err := xlsx.Build([][]any{header, {"1000000001", "X", "Balonově", "padesát tisíc"}}, exportedAt)
	require.NoError(t, err)
	_, err = Parser{}.Parse(bytes.NewReader(content))
	assert.ErrorIs(t, err, ErrInvalidValue)
}

func TestRefusesExponentsThatWouldTakeMinutesToScale(t *testing.T) {
	for _, value := range []string{"1e-2000000000", "1E25", "99999999999999999"} {
		content, err := xlsx.Build([][]any{header, {"1000000001", "X", "Balonově", n(value)}}, exportedAt)
		require.NoError(t, err)
		_, err = Parser{}.Parse(bytes.NewReader(content))
		assert.ErrorIs(t, err, ErrInvalidValue, value)
	}
}

func TestRefusesALoanListedTwice(t *testing.T) {
	row := []any{"1000000001", "X", "Balonově", n("100")}
	content, err := xlsx.Build([][]any{header, row, row}, exportedAt)
	require.NoError(t, err)
	_, err = Parser{}.Parse(bytes.NewReader(content))
	assert.ErrorIs(t, err, ErrDuplicateLoan)
}

// Reads a real export kept outside the repository; prints counts only.
func TestRealExport(t *testing.T) {
	path := os.Getenv("COINBOOK_REAL_FINGOOD")
	if path == "" {
		t.Skip("set COINBOOK_REAL_FINGOOD to a real Fingood export")
	}
	content, err := os.ReadFile(path) //nolint:gosec // G304: a path the developer chose
	require.NoError(t, err)
	st := parse(t, content)
	positions := st.Accounts[0].P2P.Positions
	byStatus := map[string]int{}
	for _, p := range positions {
		byStatus[p.Status]++
		assert.GreaterOrEqual(t, p.OutstandingMinor, int64(0))
	}
	t.Logf("%d loans on %s, by status %v", len(positions), st.Accounts[0].P2P.TakenOn.Format(time.DateOnly), byStatus)
}
