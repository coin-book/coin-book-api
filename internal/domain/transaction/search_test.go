package transaction

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchAmountReadsHowPeopleTypeAmounts(t *testing.T) {
	cases := []struct {
		query  string
		minor  int64
		signed bool
		ok     bool
	}{
		{"1249", 124900, false, true},
		{"1 249,50", 124950, false, true},
		{"1249.5", 124950, false, true},
		{"-350", -35000, true, true},
		{"+62 000", 6200000, true, true},
		{"−89,00", -8900, true, true},
		{"rohlik", 0, false, false},
		{"12.345,67", 0, false, false},
		{"1,234", 0, false, false},
		{"", 0, false, false},
	}
	for _, c := range cases {
		minor, signed, ok := searchAmount(c.query)
		assert.Equal(t, c.ok, ok, c.query)
		assert.Equal(t, c.minor, minor, c.query)
		assert.Equal(t, c.signed, signed, c.query)
	}
}
