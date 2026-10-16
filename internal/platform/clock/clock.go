package clock

import "time"

// BusinessZone is where a calendar day, a month and "today" are decided (N-6).
var BusinessZone = mustLoad("Europe/Prague")

type Clock interface {
	Now() time.Time
}

type System struct{}

func (System) Now() time.Time { return time.Now() }

type Fixed struct{ At time.Time }

func (f Fixed) Now() time.Time { return f.At }

func Today(c Clock) time.Time {
	now := c.Now().In(BusinessZone)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}
