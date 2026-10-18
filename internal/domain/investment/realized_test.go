package investment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/platform/money"
)

func TestRealizeSplitsExemptAndTaxableGainsPerYear(t *testing.T) {
	h := &history{
		instruments: map[string]Instrument{"US0378331005": {ISIN: "US0378331005", Ticker: "AAPL"}},
		sales: []LotSale{
			{ISIN: "US0378331005", BoughtOn: day(2022, 3, 2), SoldOn: day(2026, 3, 3), Quantity: qty("2"),
				ProceedsCZKMinor: czk(1150000), CostCZKMinor: czk(744088), GainCZKMinor: czk(405912)},
			{ISIN: "US0378331005", BoughtOn: day(2026, 1, 15), SoldOn: day(2026, 3, 3), Quantity: qty("1"),
				ProceedsCZKMinor: czk(575000), CostCZKMinor: czk(546000), GainCZKMinor: czk(29000)},
			{ISIN: "US0378331005", BoughtOn: day(2024, 1, 15), SoldOn: day(2025, 6, 1), Quantity: qty("1")},
		},
		dividends: []Dividend{{ISIN: "US0378331005", PaidOn: day(2026, 2, 14), Currency: money.USD,
			GrossCZKMinor: czk(5750), NetCZKMinor: czk(4876), TaxCZKMinor: 874}},
	}
	r := realize(h)
	require.Len(t, r.Years, 2)
	y2026, y2025 := r.Years[0], r.Years[1]
	assert.Equal(t, 2026, y2026.Year)
	assert.Equal(t, int64(405912), y2026.ExemptGainCZKMinor)
	assert.Equal(t, int64(29000), y2026.TaxableGainCZKMinor)
	assert.Equal(t, int64(575000), y2026.TaxableProceedsCZKMinor, "only taxable sales count toward the 100 000 Kč limit")
	assert.Equal(t, int64(434912), y2026.GainCZKMinor)
	assert.Equal(t, int64(874), y2026.DividendsTaxCZKMinor)
	assert.Equal(t, 1, y2025.MissingCZK)
	assert.Zero(t, y2025.GainCZKMinor)

	require.Len(t, r.Lots, 3)
	assert.Equal(t, day(2026, 3, 3), r.Lots[0].SoldOn, "newest sale first")
	assert.Equal(t, day(2025, 6, 1), r.Lots[2].SoldOn)
	assert.True(t, r.Lots[0].Exempt, "held from 2022")
	assert.False(t, r.Lots[1].Exempt)
}
