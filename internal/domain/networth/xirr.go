package networth

import (
	"math"
	"time"
)

const (
	xirrIterations = 200
	xirrTolerance  = 1e-9
	daysPerYear    = 365.0
)

// cashFlow is seen from the investor: money put in is negative, money taken out or held positive.
type cashFlow struct {
	on     time.Time
	amount float64
}

// xirr is the annual rate that discounts the flows to zero, found by bisection between −99.99 %
// and 1 000 % a year; false when the flows have no such rate (all of one sign) or it lies outside.
func xirr(flows []cashFlow) (float64, bool) {
	if len(flows) < 2 {
		return 0, false
	}
	var in, out bool
	for _, f := range flows {
		in = in || f.amount < 0
		out = out || f.amount > 0
	}
	if !in || !out {
		return 0, false
	}
	start := flows[0].on
	npv := func(rate float64) float64 {
		var sum float64
		for _, f := range flows {
			years := f.on.Sub(start).Hours() / 24 / daysPerYear
			sum += f.amount / math.Pow(1+rate, years)
		}
		return sum
	}
	low, high := -0.9999, 10.0
	if npv(low)*npv(high) > 0 {
		return 0, false
	}
	for range xirrIterations {
		mid := (low + high) / 2
		if npv(low)*npv(mid) <= 0 {
			high = mid
		} else {
			low = mid
		}
		if high-low < xirrTolerance {
			break
		}
	}
	return (low + high) / 2, true
}
