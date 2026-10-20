package loan

import "time"

// Deductible interest per year on loans for one's own housing, Act 586/1992 § 15(3):
// 150 000 Kč for housing acquired from 2021, 300 000 Kč before.
const (
	deductionLimitMinor      int64 = 150_000_00
	deductionLimitBefore2021 int64 = 300_000_00
)

var lowerLimitFrom = time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC)

// DeductionLimit goes by the loan's first terms, the day the housing was financed.
func DeductionLimit(firstTerms time.Time) int64 {
	if firstTerms.Before(lowerLimitFrom) {
		return deductionLimitBefore2021
	}
	return deductionLimitMinor
}

type InterestYear struct {
	Year            int
	InterestMinor   int64
	DeductibleMinor int64
}
