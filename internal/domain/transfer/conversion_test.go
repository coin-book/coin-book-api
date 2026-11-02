package transfer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlausibleConversion(t *testing.T) {
	czk, eur := 1.0, 25.0
	payment := movement{minor: -250000, currency: "CZK", czkPerUnit: &czk}

	assert.True(t, plausibleConversion(payment, movement{minor: 9800, currency: "EUR", czkPerUnit: &eur}), "within the spread")
	assert.False(t, plausibleConversion(payment, movement{minor: 350, currency: "EUR", czkPerUnit: &eur}), "a coffee is no top-up")
	assert.True(t, plausibleConversion(payment, movement{minor: 350, currency: "EUR"}), "no rate, no judgement")
	assert.True(t, plausibleConversion(movement{minor: -1000, currency: "JPY", czkPerUnit: ptr(0.16)},
		movement{minor: 16000, currency: "CZK", czkPerUnit: &czk}), "a currency without decimals")
}

func ptr(f float64) *float64 { return &f }
