package revolut

import (
	"os"
	"testing"

	"github.com/google/uuid"

	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/domain/investment"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Runs against a real export kept outside the repository; prints counts only.
func TestRealExport(t *testing.T) {
	path := os.Getenv("COINBOOK_REAL_REVOLUT")
	if path == "" {
		t.Skip("set COINBOOK_REAL_REVOLUT to a real Revolut consolidated statement")
	}
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	for _, acc := range st.Accounts {
		var sum int64
		broken := 0
		for i, r := range acc.Rows {
			sum += r.AmountMinor
			if i > 0 && r.BalanceAfterMinor != nil && *acc.Rows[i-1].BalanceAfterMinor+r.AmountMinor != *r.BalanceAfterMinor {
				broken++
			}
		}
		t.Logf("%s rows=%d balance_breaks=%d", *acc.Ref.ExternalRef, len(acc.Rows), broken)
		if inv := acc.Investments; inv != nil {
			t.Logf("%s buys=%d dividends=%d lot_sales=%d", *acc.Ref.ExternalRef, len(inv.Buys), len(inv.Dividends), len(inv.LotSales))
			logLotMatching(t, st, inv)
			for _, l := range inv.LotSales {
				if l.ProceedsCZKMinor == nil || l.CostCZKMinor == nil || l.GainCZKMinor == nil {
					t.Errorf("line %d: a sold lot without its CZK values", l.Line)
				} else {
					assert.InDelta(t, *l.ProceedsCZKMinor-*l.CostCZKMinor, *l.GainCZKMinor, 2, "line %d: CZK gain", l.Line)
				}
				assert.InDelta(t, l.ProceedsMinor-l.CostMinor, l.GainMinor, 2, "line %d: gain", l.Line)
			}
		}
		if acc.DeclaredSum != nil {
			assert.Equal(t, *acc.DeclaredSum, sum, *acc.Ref.ExternalRef)
		}
	}
}

// logLotMatching counts lots sold from a purchase inside the statement's period that find
// no such purchase. A few are expected: not every acquisition is listed as a buy.
func logLotMatching(t *testing.T, st *statement.Statement, inv *statement.Investments) {
	t.Helper()
	account := uuid.New()
	var buys []investment.Buy
	firstBuy := ""
	for _, b := range inv.Buys {
		buys = append(buys, investment.Buy{AccountID: account, ISIN: b.Security.ISIN, TradedOn: b.TradedOn, Quantity: b.Quantity})
		if d := b.TradedOn.Format("2006-01-02"); firstBuy == "" || d < firstBuy {
			firstBuy = d
		}
	}
	var inPeriod []investment.LotSale
	for _, l := range inv.LotSales {
		if firstBuy != "" && l.BoughtOn.Format("2006-01-02") >= firstBuy {
			inPeriod = append(inPeriod, investment.LotSale{AccountID: account, ISIN: l.Security.ISIN, BoughtOn: l.BoughtOn, Quantity: l.Quantity})
		}
	}
	lots, incomplete := investment.OpenLots(buys, inPeriod)
	t.Logf("lots sold from purchases in the period=%d unmatched instruments=%d open lots=%d", len(inPeriod), len(incomplete), len(lots))
}
