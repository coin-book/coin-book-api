package networth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestXIRROfAYearAtTenPercent(t *testing.T) {
	rate, ok := xirr([]cashFlow{{day(2025, 1, 1), -100_000}, {day(2026, 1, 1), 110_000}})
	assert.True(t, ok)
	assert.InDelta(t, 0.10, rate, 1e-6)
}

func TestXIRRWithDepositsDuringTheYear(t *testing.T) {
	rate, ok := xirr([]cashFlow{{day(2025, 1, 1), -10_000}, {day(2025, 7, 2), -10_000}, {day(2026, 1, 1), 21_500}})
	assert.True(t, ok)
	assert.InDelta(t, 0.1007, rate, 0.0005, "the second deposit worked half the year")
}

func TestXIRRNeedsMoneyInAndOut(t *testing.T) {
	_, ok := xirr([]cashFlow{{day(2025, 1, 1), -100}, {day(2025, 6, 1), -100}})
	assert.False(t, ok)
	_, ok = xirr([]cashFlow{{day(2025, 1, 1), -100}})
	assert.False(t, ok)
}
