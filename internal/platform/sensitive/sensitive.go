// Package sensitive holds value types that must never reach a log in full (N-2).
package sensitive

import (
	"log/slog"
)

const visibleSuffix = 4

type AccountNumber string

func (a AccountNumber) LogValue() slog.Value { return slog.StringValue(Mask(string(a))) }

func (a AccountNumber) String() string { return Mask(string(a)) }

type MessageText string

func (MessageText) LogValue() slog.Value { return slog.StringValue("[redacted]") }

func (MessageText) String() string { return "[redacted]" }

// Mask keeps the last four characters, enough to tell accounts apart in a log.
func Mask(value string) string {
	runes := []rune(value)
	if len(runes) <= visibleSuffix {
		return "****"
	}
	return "****" + string(runes[len(runes)-visibleSuffix:])
}
