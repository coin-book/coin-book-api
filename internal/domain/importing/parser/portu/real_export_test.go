package portu

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// Reads a real export kept outside the repository; prints counts only.
func TestRealExport(t *testing.T) {
	path := os.Getenv("COINBOOK_REAL_PORTU")
	if path == "" {
		t.Skip("set COINBOOK_REAL_PORTU to a real Portu transaction history")
	}
	f, err := os.Open(path) //nolint:gosec // G304: a path the developer chose
	require.NoError(t, err)
	defer f.Close()
	st, err := Parser{}.Parse(f)
	require.NoError(t, err)
	inv := st.Accounts[0].Investments
	unmatched, withoutCZK := 0, 0
	for _, s := range inv.LotSales {
		if s.BoughtOn.Equal(s.SoldOn) {
			unmatched++
		}
		if s.GainCZKMinor == nil {
			withoutCZK++
		}
	}
	buysWithoutCZK := 0
	for _, b := range inv.Buys {
		if b.NetCZKMinor == nil {
			buysWithoutCZK++
		}
	}
	t.Logf("%d buys (%d without CZK), %d sold lots (%d unmatched, %d without CZK), %d dividends, %s – %s",
		len(inv.Buys), buysWithoutCZK, len(inv.LotSales), unmatched, withoutCZK, len(inv.Dividends),
		st.PeriodFrom.Format("2006-01"), st.PeriodTo.Format("2006-01"))
}
