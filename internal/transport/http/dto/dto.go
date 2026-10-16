// Package dto holds wire helpers shared by the handler packages.
package dto

import (
	"time"

	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
)

const monthLayout = "2006-01"

func Date(t time.Time) string { return t.Format(time.DateOnly) }

func DatePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := Date(*t)
	return &s
}

func ParseDate(raw, location string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		return nil, apierr.Validation(location, "expected YYYY-MM-DD")
	}
	return &t, nil
}

func ParseMonth(raw string) (time.Time, error) {
	t, err := time.Parse(monthLayout, raw)
	if err != nil {
		return time.Time{}, apierr.Validation("path.month", "expected YYYY-MM")
	}
	return t, nil
}

func Month(t time.Time) string { return t.Format(monthLayout) }

func Currency(raw string) (money.Currency, error) {
	if raw == "" {
		return money.CZK, nil
	}
	c := money.Currency(raw)
	if !c.Valid() {
		return "", apierr.Unprocessable(apierr.CodeInvalidCurrency)
	}
	return c, nil
}
