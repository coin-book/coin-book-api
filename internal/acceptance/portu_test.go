//go:build integration

package acceptance

import (
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func portuExport(t *testing.T) []byte {
	t.Helper()
	content, err := os.ReadFile("../domain/importing/parser/portu/testdata/Transakcni_historie.csv")
	require.NoError(t, err)
	return content
}

func TestPortuTradesBecomePositionsWithLotsMatchedFirstInFirstOut(t *testing.T) {
	c := newClient(t)
	var imported importResult
	c.upload("/api/v1/imports", "Transakcni_historie.csv", portuExport(t), http.StatusCreated, &imported)
	require.Len(t, imported.Accounts, 1)
	assert.Equal(t, 6, imported.RowsNew, "3 buys, 2 sold lots and a dividend")

	var p portfolio
	c.do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, &p)
	quantities := map[string]string{}
	for _, pos := range p.Positions {
		quantities[pos.Instrument.Ticker] = pos.Quantity
		if pos.Instrument.Ticker == "CSPX" {
			require.Len(t, pos.Lots, 1)
			assert.Equal(t, "2026-01-05", pos.Lots[0].BoughtOn, "the 2022 lot moved between portfolios was sold first")
			require.NotNil(t, pos.Instrument.YahooSymbol)
			assert.Equal(t, "CSPX.L", *pos.Instrument.YahooSymbol)
		}
	}
	assert.Equal(t, map[string]string{"CSPX": "0.5", "CEZ": "2"}, quantities)
	assert.Empty(t, p.IncompleteHistory)

	var realized struct {
		Years []struct {
			Year               int                   `json:"year"`
			ExemptGainCZK      struct{ Minor int64 } `json:"exempt_gain_czk"`
			TaxableGainCZK     struct{ Minor int64 } `json:"taxable_gain_czk"`
			TaxableProceedsCZK struct{ Minor int64 } `json:"taxable_proceeds_czk"`
			DividendsTaxCZK    struct{ Minor int64 } `json:"dividends_tax_czk"`
		} `json:"years"`
	}
	c.do(http.MethodGet, "/api/v1/investments/realized", nil, http.StatusOK, &realized)
	require.Len(t, realized.Years, 1)
	y := realized.Years[0]
	assert.Equal(t, 2026, y.Year)
	assert.Equal(t, int64(13_200_00-8_600_00), y.ExemptGainCZK.Minor, "held since 2022: 600 $ at 22 Kč less 400 $ at 21.50")
	assert.Equal(t, int64(6_600_00-5_720_00), y.TaxableGainCZK.Minor)
	assert.Equal(t, int64(6_600_00), y.TaxableProceedsCZK.Minor)
	assert.Equal(t, int64(33_00), y.DividendsTaxCZK.Minor)

	c.upload("/api/v1/imports", "Transakcni_historie (1).csv", portuExport(t), http.StatusConflict, nil)
	c.do(http.MethodPost, "/api/v1/imports/"+imported.BatchID+"/rollback", nil, http.StatusOK, nil)
	c.do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, &p)
	assert.Empty(t, p.Positions)
}
