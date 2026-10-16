package money

import (
	"strconv"
	"strings"
)

const minusSign = "−"

const thousandsSeparator = " "

var currencyLabels = map[Currency]string{CZK: "Kč", EUR: "€", USD: "$"}

// Format renders an amount the way the web client does; twin of
// coin-book-web/packages/core/src/money.ts, pinned by testdata/format-vectors.json.
func Format(a Amount) string {
	value := a.Minor
	sign := ""
	if value < 0 {
		sign = minusSign
		value = -value
	}
	exponent := a.Currency.Exponent()
	digits := strconv.FormatInt(value, 10)
	if len(digits) <= exponent {
		digits = strings.Repeat("0", exponent-len(digits)+1) + digits
	}
	whole := digits[:len(digits)-exponent]
	text := groupThousands(whole)
	if exponent > 0 {
		text += "," + digits[len(digits)-exponent:]
	}
	return sign + text + " " + label(a.Currency)
}

func label(c Currency) string {
	if l, ok := currencyLabels[c]; ok {
		return l
	}
	return string(c)
}

func groupThousands(whole string) string {
	if len(whole) <= 3 {
		return whole
	}
	var b strings.Builder
	head := len(whole) % 3
	if head > 0 {
		b.WriteString(whole[:head])
	}
	for i := head; i < len(whole); i += 3 {
		if b.Len() > 0 {
			b.WriteString(thousandsSeparator)
		}
		b.WriteString(whole[i : i+3])
	}
	return b.String()
}
