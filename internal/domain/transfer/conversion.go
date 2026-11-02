package transfer

import (
	"math"

	"coin-book-api/internal/platform/money"
)

// conversionTolerance covers a bank's spread and a rate a few days off; a payment that far off is something else.
const conversionTolerance = 0.10

type movement struct {
	minor    int64
	currency money.Currency
	// czkPerUnit is nil when no exchange rate is known for the currency.
	czkPerUnit *float64
}

// czkPerUnitSQL is the ČNB rate of currency on day, or the earliest one when the day precedes them all;
// NULL when none is stored, e.g. with market data turned off.
func czkPerUnitSQL(currency, day string) string {
	return `CASE WHEN ` + currency + ` = 'CZK' THEN 1::float8 ELSE coalesce(
		(SELECT r.czk_per_unit::float8 FROM fx_rate r WHERE r.currency = ` + currency + ` AND r.day <= ` + day + `
			ORDER BY r.day DESC LIMIT 1),
		(SELECT r.czk_per_unit::float8 FROM fx_rate r WHERE r.currency = ` + currency + ` ORDER BY r.day LIMIT 1)) END`
}

// plausibleConversion: the two movements could be one sum exchanged at about the ČNB rate.
// Without a rate for either currency it cannot tell, so it allows them.
func plausibleConversion(a, b movement) bool {
	if a.czkPerUnit == nil || b.czkPerUnit == nil {
		return true
	}
	aCZK, bCZK := inCZK(a), inCZK(b)
	if aCZK == 0 || bCZK == 0 {
		return false
	}
	return math.Abs(aCZK-bCZK) <= conversionTolerance*math.Max(aCZK, bCZK)
}

func inCZK(m movement) float64 {
	units := math.Abs(float64(m.minor)) / math.Pow10(m.currency.Exponent())
	return units * *m.czkPerUnit
}
