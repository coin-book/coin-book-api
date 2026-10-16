package money

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMinor(t *testing.T) {
	cases := []struct {
		raw    string
		format NumberFormat
		want   int64
	}{
		{"-1234,56 CZK", CommaDecimal, -123456},
		{"-1 234,56 CZK", CommaDecimal, -123456},
		{"-12,34€", CommaDecimal, -1234},
		{"123,45$", CommaDecimal, 12345},
		{"0,00 CZK", CommaDecimal, 0},
		{"62000", CommaDecimal, 6200000},
		{"12,5", CommaDecimal, 1250},
		{"US$1,234.56", DotDecimal, 123456},
		{"12,345.67 CZK", DotDecimal, 1234567},
		{"−5,00", CommaDecimal, -500},
	}
	for _, c := range cases {
		got, err := ParseMinor(c.raw, c.format, 2)
		require.NoError(t, err, c.raw)
		assert.Equal(t, c.want, got, c.raw)
	}
}

func TestParseMinorRejects(t *testing.T) {
	for _, raw := range []string{"", "CZK", "1,234,5", "1,234"} {
		_, err := ParseMinor(raw, CommaDecimal, 2)
		assert.ErrorIs(t, err, ErrInvalidNumber, raw)
	}
}

func TestAddRefusesMixedCurrencies(t *testing.T) {
	_, err := New(100, CZK).Add(New(100, EUR))
	assert.ErrorIs(t, err, ErrCurrencyMismatch)
}

func TestFormatVectors(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/format-vectors.json")
	require.NoError(t, err)
	var vectors []struct {
		Minor     int64    `json:"minor"`
		Currency  Currency `json:"currency"`
		Formatted string   `json:"formatted"`
	}
	require.NoError(t, json.Unmarshal(raw, &vectors))
	for _, v := range vectors {
		assert.Equal(t, v.Formatted, Format(New(v.Minor, v.Currency)), "%d %s", v.Minor, v.Currency)
	}
}
