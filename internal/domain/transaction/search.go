package transaction

import (
	"regexp"
	"strconv"
	"strings"
)

// amountPattern: an optional sign, up to twelve integer digits, and at most two
// decimals after a comma or a dot. Group separators are stripped before matching.
var amountPattern = regexp.MustCompile(`^([-−+]?)(\d{1,12})(?:[.,](\d{1,2}))?$`)

const searchExponent = 2

// searchAmount reads the query as a money amount in major units. ok is false when
// the query is not a number. signed reports an explicit sign: "-350" matches only
// expenses of 350, "350" matches income and expenses alike.
func searchAmount(query string) (minor int64, signed, ok bool) {
	compact := strings.NewReplacer(" ", "", " ", "", " ", "", "'", "").Replace(strings.TrimSpace(query))
	m := amountPattern.FindStringSubmatch(compact)
	if m == nil {
		return 0, false, false
	}
	frac := m[3] + strings.Repeat("0", searchExponent-len(m[3]))
	value, err := strconv.ParseInt(m[2]+frac, 10, 64)
	if err != nil {
		return 0, false, false
	}
	if m[1] == "-" || m[1] == "−" {
		value = -value
	}
	return value, m[1] != "", true
}
