package seed

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
