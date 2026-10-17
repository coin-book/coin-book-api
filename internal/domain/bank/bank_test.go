package bank

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCandidateID(t *testing.T) {
	cases := map[string]string{
		"19-1234567890/6210":            "cz-6210",
		"1234567890/0800":               "cz-0800",
		"CZ65 0800 0000 1920 0014 5399": "cz-0800",
		"SK3112000000198742637541":      "sk-1200",
		"SK89 7500 0000 0000 1234 5671": "sk-7500",
		"LT12 1000 0111 0100 1000":      "",
		"csv:Hotovost":                  "",
	}
	for number, want := range cases {
		assert.Equal(t, want, CandidateID(number), number)
	}
}

func TestCanonicalNumberMatchesTheWayStatementsPrintCounterparties(t *testing.T) {
	for in, want := range map[string]string{
		"000019-0000123457/0800":        "19-123457/0800",
		"19-123457/0800":                "19-123457/0800",
		"123457 / 0800":                 "123457/0800",
		"CZ65 0800 0000 1920 0014 5399": "19-2000145399/0800",
		"SK31 1200 0000 1987 4263 7541": "19-8742637541/1200",
		"GB33BUKB20201555555555":        "GB33BUKB20201555555555",
	} {
		assert.Equal(t, want, CanonicalNumber(in), in)
	}
}
