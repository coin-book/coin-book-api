package budget

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProgressCountsTodayAsLeft(t *testing.T) {
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	elapsed, left := progress(october, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	assert.InDelta(t, 15.0/31.0, elapsed, 1e-9)
	assert.Equal(t, 16, left)

	elapsed, left = progress(october, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, 1.0, elapsed)
	assert.Equal(t, 0, left)

	elapsed, left = progress(october, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, 0.0, elapsed)
	assert.Equal(t, 31, left)
}

func TestAssess(t *testing.T) {
	line := Line{PlannedMinor: 10000}

	assert.Equal(t, OnTrack, assess(line, 4000, 0.5, true).Status)
	assert.Equal(t, AheadOfPace, assess(line, 6000, 0.5, true).Status)
	assert.Equal(t, Over, assess(line, 10001, 0.5, true).Status)
	assert.Equal(t, OnTrack, assess(line, 9000, 1, true).Status, "a finished month has no pace to be ahead of")
	assert.Equal(t, int64(12000), assess(line, 6000, 0.5, true).ProjectedMinor)
	assert.Equal(t, OnTrack, assess(line, 9000, 0.5, false).Status, "a monthly bill paid early is not overspending")
}
