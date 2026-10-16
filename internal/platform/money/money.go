package money

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/rotisserie/eris"
)

type Currency string

const (
	CZK Currency = "CZK"
	EUR Currency = "EUR"
	USD Currency = "USD"
)

var zeroDecimalCurrencies = map[Currency]bool{"JPY": true, "KRW": true, "ISK": true}

func (c Currency) Exponent() int {
	if zeroDecimalCurrencies[c] {
		return 0
	}
	return 2
}

func (c Currency) Valid() bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

type Amount struct {
	Minor    int64    `json:"minor" doc:"Amount in the currency's smallest unit (haléře, cents)."`
	Currency Currency `json:"currency" doc:"ISO 4217 code." minLength:"3" maxLength:"3"`
}

var ErrCurrencyMismatch = eris.New("money: currencies differ")

func New(minor int64, currency Currency) Amount { return Amount{Minor: minor, Currency: currency} }

func (a Amount) Add(b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, eris.Wrapf(ErrCurrencyMismatch, "%s + %s", a.Currency, b.Currency)
	}
	return Amount{Minor: a.Minor + b.Minor, Currency: a.Currency}, nil
}

func (a Amount) Neg() Amount { return Amount{Minor: -a.Minor, Currency: a.Currency} }

func (a Amount) IsZero() bool { return a.Minor == 0 }

type NumberFormat struct {
	Decimal rune
	Group   []rune
}

var (
	CommaDecimal = NumberFormat{Decimal: ',', Group: []rune{' ', ' ', ' ', '.'}}
	DotDecimal   = NumberFormat{Decimal: '.', Group: []rune{',', ' ', ' '}}
)

var ErrInvalidNumber = eris.New("money: invalid number")

// ParseMinor reads a decimal number into minor units of the given exponent.
// Anything that is not a digit, sign, decimal or group separator (currency codes
// and symbols) is ignored, so "-1 234,56 CZK", "-12,34€" and "US$1,234.56" all parse.
func ParseMinor(raw string, format NumberFormat, exponent int) (int64, error) {
	var intPart, fracPart strings.Builder
	negative := false
	seenDecimal := false
	seenDigit := false
	for _, r := range raw {
		switch {
		case r == '-' || r == '−':
			if seenDigit {
				return 0, eris.Wrapf(ErrInvalidNumber, "%q", raw)
			}
			negative = true
		case r == format.Decimal:
			if seenDecimal {
				return 0, eris.Wrapf(ErrInvalidNumber, "%q", raw)
			}
			seenDecimal = true
		case unicode.IsDigit(r):
			seenDigit = true
			if seenDecimal {
				fracPart.WriteRune(r)
			} else {
				intPart.WriteRune(r)
			}
		case isGroupSeparator(r, format):
		}
	}
	if !seenDigit {
		return 0, eris.Wrapf(ErrInvalidNumber, "%q", raw)
	}
	frac := fracPart.String()
	if len(frac) > exponent {
		return 0, eris.Wrapf(ErrInvalidNumber, "%q has more than %d decimals", raw, exponent)
	}
	frac += strings.Repeat("0", exponent-len(frac))
	digits := intPart.String() + frac
	if digits == "" {
		digits = "0"
	}
	value, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, eris.Wrapf(ErrInvalidNumber, "%q", raw)
	}
	if negative {
		value = -value
	}
	return value, nil
}

func isGroupSeparator(r rune, format NumberFormat) bool {
	for _, g := range format.Group {
		if r == g {
			return true
		}
	}
	return false
}
