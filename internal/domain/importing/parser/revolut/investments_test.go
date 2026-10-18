package revolut

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/testutil"
)

func parseInvestments(t *testing.T) *statement.Statement {
	t.Helper()
	f, err := os.Open("testdata/investments.csv")
	require.NoError(t, err)
	defer f.Close()
	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	return st
}

func TestParseInvestmentsGolden(t *testing.T) {
	testutil.AssertGoldenJSON(t, "testdata/investments.golden.json", parseInvestments(t))
}

func TestParseInvestmentsSplitsBrokerAndRobo(t *testing.T) {
	st := parseInvestments(t)
	require.Len(t, st.Accounts, 2, "summaries and crypto are skipped")
	broker, robo := st.Accounts[0], st.Accounts[1]
	assert.Equal(t, "revolut:broker", *broker.Ref.ExternalRef)
	assert.Equal(t, "revolut:robo", *robo.Ref.ExternalRef)
	assert.Empty(t, broker.Rows)
	assert.True(t, broker.HasRecords())

	require.Len(t, broker.Investments.Buys, 2, "identical buys on one day both count")
	require.Len(t, broker.Investments.LotSales, 2)
	require.Len(t, broker.Investments.Dividends, 1)
	require.Len(t, robo.Investments.Buys, 1)
	require.Len(t, robo.Investments.LotSales, 1)
	require.Len(t, robo.Investments.Dividends, 1)
}

func TestParseInvestmentBuy(t *testing.T) {
	b := parseInvestments(t).Accounts[0].Investments.Buys[0]
	assert.Equal(t, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), b.TradedOn)
	assert.Equal(t, statement.Security{ISIN: "US0378331005", Ticker: "AAPL"}, b.Security)
	assert.Equal(t, "3", b.Quantity.String())
	assert.Equal(t, money.USD, b.Currency)
	assert.Equal(t, int64(78000), b.GrossMinor)
	assert.Equal(t, int64(78150), b.NetMinor)
	assert.Equal(t, int64(1797450), *b.NetCZKMinor)
	assert.Equal(t, int64(3450), b.FeeCZKMinor)
}

func TestParseInvestmentLotSale(t *testing.T) {
	l := parseInvestments(t).Accounts[0].Investments.LotSales[0]
	assert.Equal(t, time.Date(2022, 3, 2, 0, 0, 0, 0, time.UTC), l.BoughtOn)
	assert.Equal(t, time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), l.SoldOn)
	assert.Equal(t, statement.Security{ISIN: "US0378331005", Ticker: "AAPL", Name: "Apple Inc"}, l.Security)
	assert.Equal(t, int64(50000), l.ProceedsMinor)
	assert.Equal(t, int64(33100), l.CostMinor, "cost is positive")
	assert.Equal(t, int64(16900), l.GainMinor)
	assert.Equal(t, int64(744088), *l.CostCZKMinor)
	assert.Equal(t, int64(385912), *l.GainCZKMinor)
	assert.Equal(t, int64(496), l.FeeCZKMinor, "a fee charged in euros on a dollar trade is taken in CZK")

	loss := parseInvestments(t).Accounts[0].Investments.LotSales[1]
	assert.Equal(t, int64(-1000), loss.GainMinor)
}

func TestParseInvestmentFractionalRoboLot(t *testing.T) {
	l := parseInvestments(t).Accounts[1].Investments.LotSales[0]
	assert.Equal(t, "0.12345678", l.Quantity.String())
	assert.Equal(t, money.EUR, l.Currency)
	assert.Equal(t, "EUNL", l.Security.Ticker)
	assert.Equal(t, "iShares Core MSCI World UCITS ETF (Acc)", l.Security.Name)
}

func TestParseInvestmentDividendUsesSlovakNumbers(t *testing.T) {
	st := parseInvestments(t)
	d := st.Accounts[0].Investments.Dividends[0]
	assert.Equal(t, "Apple", d.Security.Name)
	assert.Equal(t, int64(250), d.GrossMinor)
	assert.Equal(t, int64(212), d.NetMinor)
	assert.Equal(t, int64(874), d.TaxCZKMinor)

	robo := st.Accounts[1].Investments.Dividends[0]
	assert.Equal(t, int64(123456), robo.GrossMinor)
	assert.Equal(t, int64(3086400), *robo.GrossCZKMinor)
	assert.Zero(t, robo.TaxCZKMinor, "a CZK column written in euros is not read as CZK")
}

func TestParseInvestmentIdentityKeysTellIdenticalBuysApartOnlyByOrder(t *testing.T) {
	buys := parseInvestments(t).Accounts[0].Investments.Buys
	assert.Equal(t, buys[0].IdentityKey, buys[1].IdentityKey)
	assert.Equal(t, "buy|2026-01-15|US0378331005|3|78000", buys[0].IdentityKey)
}

func TestCurrencyOf(t *testing.T) {
	for raw, want := range map[string]money.Currency{
		"+US$1,234.56": money.USD, "12,34$": money.USD, "-€9.99": money.EUR, "1 234,00 CZK": money.CZK, "£3": "GBP",
	} {
		got, ok := currencyOf(raw)
		assert.True(t, ok, raw)
		assert.Equal(t, want, got, raw)
	}
	_, ok := currencyOf("12.00")
	assert.False(t, ok)
}

func TestParseInvestmentRefusesHostileValues(t *testing.T) {
	row := func(isin, quantity string) investmentRow {
		fields := []string{"15. 1. 2026", "AAPL", isin, quantity, "United States", "US$780", "17,940 CZK"}
		return investmentRow{fields: fields, format: money.DotDecimal, columns: columnPositions(
			[]string{"Dátum", "Popis a symbol", "ISIN", "Množstvo", "Krajina", "Hrubá hodnota", "Hrubá hodnota"})}
	}
	for _, quantity := range []string{"1e-2147483000", "1E9", "-1", "0", "1234567890123456789", "1.12345678901"} {
		_, err := row("US0378331005", quantity).buy(1)
		assert.Error(t, err, quantity)
	}
	_, err := row("US0378331006", "1").buy(1)
	assert.Error(t, err, "a wrong check digit")
	b, err := row("US0378331005", "1,000.5").buy(1)
	require.NoError(t, err)
	assert.Equal(t, "1000.5", b.Quantity.String())
}

func TestSecurityDropsWhatIsNotATickerOrName(t *testing.T) {
	assert.Equal(t, "", security("US0378331005", "CALL +420 123", "").Ticker)
	assert.Equal(t, "BRK.B", security("US0378331005", "brk.b", "").Ticker)
	assert.Len(t, []rune(security("US0378331005", "", strings.Repeat("ž", 500)).Name), maxNameLength)
}
