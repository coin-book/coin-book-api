package investment

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"coin-book-api/internal/platform/clock"
)

func TestNextRefreshIsTheNextSetHourInPrague(t *testing.T) {
	prague := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, clock.BusinessZone) }
	assert.Equal(t, prague(17, 23, 0), nextRefresh(prague(17, 9, 0), 23))
	assert.Equal(t, prague(18, 23, 0), nextRefresh(prague(17, 23, 0), 23), "the hour itself has just run")
	assert.Equal(t, prague(18, 23, 0), nextRefresh(prague(17, 23, 30).UTC(), 23))
}

func TestYahooSymbolGuess(t *testing.T) {
	assert.Equal(t, "BRK-B", *YahooSymbol("BRK.B", "USD"))
	assert.Equal(t, "EUNL.DE", *YahooSymbol("eunl", "EUR"))
	assert.Nil(t, YahooSymbol("VUSA", "GBP"))
	assert.Nil(t, YahooSymbol("", "USD"))
}
