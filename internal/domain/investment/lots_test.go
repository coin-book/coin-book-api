package investment

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/platform/money"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func qty(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func czk(v int64) *int64 { return &v }

func TestTimeTestNeedsMoreThanThreeYears(t *testing.T) {
	bought := day(2022, 3, 2)
	assert.False(t, PassesTimeTest(bought, day(2025, 3, 2)), "exactly three years is not more than three")
	assert.True(t, PassesTimeTest(bought, day(2025, 3, 3)))
	assert.Equal(t, day(2025, 3, 3), ExemptFrom(bought))
}

func TestTimeTestFromALeapDay(t *testing.T) {
	assert.Equal(t, day(2023, 3, 1), ExemptFrom(day(2020, 2, 29)))
	assert.True(t, PassesTimeTest(day(2020, 2, 29), day(2023, 3, 1)))
	assert.Equal(t, day(2027, 1, 1), ExemptFrom(day(2023, 12, 31)))
}

func TestOpenLotsSubtractsSoldLotsFromTheirPurchaseDay(t *testing.T) {
	account := uuid.New()
	buys := []Buy{
		{AccountID: account, ISIN: "US0378331005", TradedOn: day(2022, 3, 2), Quantity: qty("2"), Currency: money.USD, NetMinor: 33100, NetCZKMinor: czk(744088)},
		{AccountID: account, ISIN: "US0378331005", TradedOn: day(2026, 1, 15), Quantity: qty("3"), Currency: money.USD, NetMinor: 78150, NetCZKMinor: czk(1797450)},
		{AccountID: account, ISIN: "US0378331005", TradedOn: day(2026, 1, 15), Quantity: qty("3"), Currency: money.USD, NetMinor: 78150, NetCZKMinor: czk(1797450)},
	}
	sales := []LotSale{
		{AccountID: account, ISIN: "US0378331005", BoughtOn: day(2022, 3, 2), SoldOn: day(2026, 3, 3), Quantity: qty("2")},
		{AccountID: account, ISIN: "US0378331005", BoughtOn: day(2026, 1, 15), SoldOn: day(2026, 3, 3), Quantity: qty("1.5")},
	}
	lots, incomplete := OpenLots(buys, sales)
	assert.Empty(t, incomplete)
	require.Len(t, lots, 1, "the 2022 purchase is fully sold")
	assert.Equal(t, "4.5", lots[0].Quantity.String())
	assert.Equal(t, int64(117225), lots[0].CostMinor, "three quarters of the day's cost")
	assert.Equal(t, int64(2696175), *lots[0].CostCZKMinor)
}

func TestOpenLotsFlagsSalesWithoutTheirPurchase(t *testing.T) {
	account := uuid.New()
	sales := []LotSale{{AccountID: account, ISIN: "IE00B4L5Y983", BoughtOn: day(2021, 12, 20), SoldOn: day(2026, 1, 5), Quantity: qty("1")}}
	lots, incomplete := OpenLots(nil, sales)
	assert.Empty(t, lots)
	assert.True(t, incomplete["IE00B4L5Y983"])
}

func TestOpenLotsKeepsAccountsApart(t *testing.T) {
	broker, robo := uuid.New(), uuid.New()
	buys := []Buy{{AccountID: broker, ISIN: "IE00B4L5Y983", TradedOn: day(2026, 2, 2), Quantity: qty("1"), Currency: money.EUR, NetMinor: 10000}}
	sales := []LotSale{{AccountID: robo, ISIN: "IE00B4L5Y983", BoughtOn: day(2026, 2, 2), SoldOn: day(2026, 3, 1), Quantity: qty("1")}}
	lots, incomplete := OpenLots(buys, sales)
	require.Len(t, lots, 1)
	assert.Nil(t, lots[0].CostCZKMinor, "no CZK value without one on the purchase")
	assert.True(t, incomplete["IE00B4L5Y983"])
}

func TestMissingPurchasesAreWhatTheSalesTookBeyondTheImportedBuys(t *testing.T) {
	account := uuid.New()
	buys := []Buy{{AccountID: account, ISIN: "US0378331005", TradedOn: day(2022, 1, 19), Quantity: qty("1"), Currency: money.USD}}
	sales := []LotSale{
		{AccountID: account, ISIN: "US0378331005", BoughtOn: day(2021, 12, 21), SoldOn: day(2025, 10, 20), Quantity: qty("1"),
			Currency: money.USD, CostMinor: 17200},
		{AccountID: account, ISIN: "US0378331005", BoughtOn: day(2022, 1, 19), SoldOn: day(2025, 10, 20), Quantity: qty("1.5"),
			Currency: money.USD, CostMinor: 30000},
	}
	assert.Equal(t, []MissingBuy{
		{AccountID: account, ISIN: "US0378331005", BoughtOn: day(2021, 12, 21), Quantity: qty("1"), Currency: money.USD, CostMinor: 17200},
		{AccountID: account, ISIN: "US0378331005", BoughtOn: day(2022, 1, 19), Quantity: qty("0.5"), Currency: money.USD, CostMinor: 10000},
	}, MissingPurchases(buys, sales))
	assert.Empty(t, MissingPurchases(append(buys, Buy{AccountID: account, ISIN: "US0378331005", TradedOn: day(2021, 12, 21),
		Quantity: qty("2")}, Buy{AccountID: account, ISIN: "US0378331005", TradedOn: day(2022, 1, 19), Quantity: qty("0.5")}), sales))
}
