package seed

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/domain/importing/parser/fingood"
	"coin-book-api/internal/domain/importing/parser/revolut"
)

func TestDemoInvestmentsParse(t *testing.T) {
	st, err := revolut.Parser{}.Parse(strings.NewReader(demoInvestmentsCSV))
	require.NoError(t, err)
	require.Len(t, st.Accounts, 2)
	assert.Len(t, st.Accounts[0].Investments.Buys, 2)
	assert.Len(t, st.Accounts[0].Investments.LotSales, 2)
	assert.Len(t, st.Accounts[0].Investments.Dividends, 1)
	assert.Len(t, st.Accounts[1].Investments.Buys, 4)
}

func TestDemoFingoodExportParses(t *testing.T) {
	export, err := demoFingoodExport(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	st, err := fingood.Parser{}.Parse(bytes.NewReader(export))
	require.NoError(t, err)
	require.Len(t, st.Accounts, 1)
	assert.Len(t, st.Accounts[0].P2P.Positions, 6)
	assert.Equal(t, "2026-10-18", st.Accounts[0].P2P.TakenOn.Format(time.DateOnly))
}
