package revolut

import (
	"os"
	"testing"

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
		if acc.DeclaredSum != nil {
			assert.Equal(t, *acc.DeclaredSum, sum, *acc.Ref.ExternalRef)
		}
	}
}
