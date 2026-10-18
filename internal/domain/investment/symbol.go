// Package investment holds investments (ADR 209): positions, open lots and the
// three-year time test, realised gains, and prices from market data.
package investment

import (
	"strings"

	"coin-book-api/internal/platform/money"
)

// YahooSymbol guesses an instrument's Yahoo symbol from how the broker trades it:
// dollar instruments are on US exchanges under their ticker, euro ETFs on Xetra.
// Nil when there is no good guess; the user can set it.
func YahooSymbol(ticker string, currency money.Currency) *string {
	ticker = strings.TrimSpace(strings.ToUpper(ticker))
	if ticker == "" {
		return nil
	}
	var symbol string
	switch currency {
	case money.USD:
		symbol = strings.ReplaceAll(ticker, ".", "-")
	case money.EUR:
		symbol = ticker + ".DE"
	default:
		return nil
	}
	return &symbol
}
