package loan

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func mortgage() Terms {
	return Terms{
		ValidFrom: day(2025, 1, 1), BalanceMinor: 3_000_000_00, AnnualRate: decimal.RequireFromString("5"),
		PaymentMinor: 16_104_65, PaymentDay: 15, MaturityOn: day(2054, 12, 15),
	}
}

func TestAnnuityOfAThirtyYearMortgage(t *testing.T) {
	assert.Equal(t, int64(16_104_65), Annuity(3_000_000_00, decimal.RequireFromString("5"), 360))
	assert.Equal(t, int64(1_000_00), Annuity(12_000_00, decimal.Zero, 12))
}

func TestInstalmentsDueByTodayCountAsPaidByTheSchedule(t *testing.T) {
	s := Build([]Terms{mortgage()}, nil, nil, nil, day(2025, 3, 1))

	require.GreaterOrEqual(t, len(s.Instalments), 3)
	jan, feb, mar := s.Instalments[0], s.Instalments[1], s.Instalments[2]
	assert.Equal(t, day(2025, 1, 15), jan.DueOn)
	assert.Equal(t, int64(12_500_00), jan.InterestMinor, "a twelfth of 5 % of three million")
	assert.Equal(t, int64(3_604_65), jan.PrincipalMinor)
	assert.Equal(t, int64(2_996_395_35), jan.BalanceMinor)
	assert.Equal(t, int64(12_484_98), feb.InterestMinor)
	assert.False(t, jan.Paid || jan.Projected)
	assert.True(t, mar.Projected)
	assert.Equal(t, feb.BalanceMinor, s.BalanceOn(day(2025, 2, 20)))
	assert.Equal(t, int64(3_000_000_00), s.BalanceOn(day(2025, 1, 14)))
	assert.Zero(t, s.BalanceOn(day(2024, 12, 31)), "nothing is owed before the loan")
}

func TestTheScheduleRunsToMaturity(t *testing.T) {
	s := Build([]Terms{mortgage()}, nil, nil, nil, day(2025, 3, 1))

	last := s.Instalments[len(s.Instalments)-1]
	assert.Len(t, s.Instalments, 360)
	assert.Equal(t, day(2054, 12, 15), last.DueOn)
	assert.Zero(t, last.BalanceMinor)
	assert.Zero(t, s.BalanceOn(day(2055, 1, 1)))
}

func TestAPaymentInTheMonthPaysInterestFirstThenPrincipal(t *testing.T) {
	payments := []Payment{
		{TransactionID: "tx_mar", BookedOn: day(2025, 3, 14), AmountMinor: 20_000_00},
		{TransactionID: "tx_extra", BookedOn: day(2025, 3, 28), AmountMinor: 50_000_00},
	}
	s := Build([]Terms{mortgage()}, nil, payments, nil, day(2025, 4, 1))

	mar := s.Instalments[2]
	assert.True(t, mar.Paid)
	assert.Equal(t, Split{InterestMinor: 12_469_90, PrincipalMinor: 7_530_10}, s.Splits["tx_mar"])
	assert.Equal(t, Split{PrincipalMinor: 50_000_00}, s.Splits["tx_extra"], "a second payment in the month is all principal")
	assert.Equal(t, s.Instalments[1].BalanceMinor-7_530_10-50_000_00, mar.BalanceMinor, "the row shows the balance after all it was paid")
	assert.Equal(t, s.Instalments[1].BalanceMinor-7_530_10, s.BalanceOn(day(2025, 3, 27)))
	assert.Equal(t, mar.BalanceMinor, s.BalanceOn(day(2025, 3, 28)), "the extra payment from the day it was made")
}

func TestTheUsersOwnSplitIsKept(t *testing.T) {
	own := Split{InterestMinor: 12_000_00, PrincipalMinor: 4_104_65}
	s := Build([]Terms{mortgage()}, nil, []Payment{{TransactionID: "tx", BookedOn: day(2025, 1, 15), AmountMinor: 16_104_65, Fixed: &own}}, nil, day(2025, 2, 1))

	assert.Equal(t, own, s.Splits["tx"])
	assert.Equal(t, int64(3_000_000_00-4_104_65), s.Instalments[0].BalanceMinor)
}

func TestAnAnchorCorrectsTheBalance(t *testing.T) {
	anchor := Anchor{On: day(2025, 12, 31), BalanceMinor: 2_900_000_00}
	s := Build([]Terms{mortgage()}, []Anchor{anchor}, nil, nil, day(2026, 3, 1))

	assert.Equal(t, int64(2_900_000_00), s.BalanceOn(day(2026, 1, 10)))
	jan := s.Instalments[12]
	assert.Equal(t, day(2026, 1, 15), jan.DueOn)
	assert.Equal(t, int64(12_083_33), jan.InterestMinor, "interest runs on the anchored balance")
}

func TestARefixationContinuesFromItsBalanceAtTheNewRate(t *testing.T) {
	refix := mortgage()
	refix.ValidFrom, refix.BalanceMinor, refix.AnnualRate, refix.PaymentMinor = day(2026, 1, 1), 2_950_000_00, decimal.RequireFromString("4"), 14_500_00
	s := Build([]Terms{refix, mortgage()}, nil, nil, nil, day(2026, 6, 1))

	jan := s.Instalments[12]
	assert.Equal(t, int64(9_833_33), jan.InterestMinor)
	assert.Equal(t, int64(14_500_00-9_833_33), jan.PrincipalMinor)
}

func TestInterestByYearLeavesOutProjectedInstalments(t *testing.T) {
	s := Build([]Terms{mortgage()}, nil, nil, nil, day(2025, 2, 20))

	assert.Equal(t, map[int]int64{2025: 12_500_00 + 12_484_98}, s.InterestByYear())
}

func TestDeductionLimitDependsOnWhenTheHousingWasFinanced(t *testing.T) {
	assert.Equal(t, int64(300_000_00), DeductionLimit(day(2020, 12, 31)))
	assert.Equal(t, int64(150_000_00), DeductionLimit(day(2021, 1, 1)))
}

func TestPrepaymentShortensTheLoanOrLowersTheInstalment(t *testing.T) {
	p := SimulatePrepayment(mortgage(), 3_000_000_00, 0, 500_000_00, day(2025, 1, 1))

	assert.Equal(t, 360, p.Current.Months)
	assert.Equal(t, day(2054, 12, 15), p.Current.PaidOffOn)
	assert.Less(t, p.KeepPayment.Months, p.Current.Months)
	assert.Equal(t, int64(16_104_65), p.KeepPayment.PaymentMinor)
	assert.Equal(t, 360, p.KeepMaturity.Months)
	assert.Equal(t, Annuity(2_500_000_00, decimal.RequireFromString("5"), 360), p.KeepMaturity.PaymentMinor)
	assert.Less(t, p.KeepPayment.InterestMinor, p.KeepMaturity.InterestMinor, "keeping the instalment saves the most interest")
	assert.Less(t, p.KeepMaturity.InterestMinor, p.Current.InterestMinor)
}

func TestAnInstalmentBookedIntoTheNextMonthStillPaysItsOwn(t *testing.T) {
	end := mortgage()
	end.PaymentDay = 31
	payments := []Payment{{TransactionID: "jan", BookedOn: day(2025, 2, 2), AmountMinor: 16_104_65}}
	s := Build([]Terms{end}, nil, payments, nil, day(2025, 2, 20))

	jan := s.Instalments[0]
	assert.Equal(t, day(2025, 1, 31), jan.DueOn)
	assert.True(t, jan.Paid, "booked two days after the 31st, it paid January")
	assert.Equal(t, int64(12_500_00), s.Splits["jan"].InterestMinor)
	assert.Equal(t, Build([]Terms{end}, nil, nil, nil, day(2025, 2, 20)).BalanceOn(day(2025, 2, 20)), s.BalanceOn(day(2025, 2, 20)))
}

func TestTermsFromADueDayStartAfterThatDaysInstalment(t *testing.T) {
	refix := mortgage()
	refix.ValidFrom, refix.BalanceMinor = day(2026, 1, 15), 2_950_000_00
	s := Build([]Terms{mortgage(), refix}, nil, nil, nil, day(2026, 6, 1))

	assert.Equal(t, int64(2_950_000_00), s.BalanceOn(day(2026, 1, 15)), "the balance entered is what is owed after the 15th's instalment")
	assert.Equal(t, int64(2_950_000_00), s.BalanceOn(day(2026, 2, 14)))
}

func TestANewPaymentDayBeforeTheTermsStartWaitsAMonth(t *testing.T) {
	refix := mortgage()
	refix.ValidFrom, refix.BalanceMinor, refix.PaymentDay = day(2026, 1, 10), 2_950_000_00, 5
	s := Build([]Terms{mortgage(), refix}, nil, nil, nil, day(2026, 6, 1))

	assert.Equal(t, int64(2_950_000_00), s.BalanceOn(day(2026, 2, 4)), "no instalment on 5 January against the balance of the 10th")
	for _, in := range s.Instalments {
		assert.NotEqual(t, day(2026, 1, 5), in.DueOn)
	}
}

func TestAnExtraPaymentBeforeTheInstalmentIsPrincipal(t *testing.T) {
	payments := []Payment{
		{TransactionID: "jan", BookedOn: day(2025, 1, 15), AmountMinor: 16_104_65},
		{TransactionID: "extra", BookedOn: day(2025, 1, 31), AmountMinor: 100_000_00},
		{TransactionID: "feb", BookedOn: day(2025, 2, 14), AmountMinor: 16_104_65},
	}
	s := Build([]Terms{mortgage()}, nil, payments, nil, day(2025, 3, 1))

	assert.Zero(t, s.Splits["extra"].InterestMinor, "the extra payment nearer February's due day is still principal")
	assert.Positive(t, s.Splits["feb"].InterestMinor)
}

func TestTheReserveLowersTheInterestAndShortensTheLoan(t *testing.T) {
	reserve := &Reserve{moves: []reserveMove{{on: day(2025, 1, 10), amount: 600_000_00}}}
	with := Build([]Terms{mortgage()}, nil, nil, reserve, day(2025, 3, 1))
	without := Build([]Terms{mortgage()}, nil, nil, nil, day(2025, 3, 1))

	assert.Equal(t, int64(10_000_00), with.Instalments[0].InterestMinor, "5 % of 2 400 000 a year, not of 3 000 000")
	assert.Equal(t, int64(16_104_65-10_000_00), with.Instalments[0].PrincipalMinor, "the instalment stays, more of it repays")
	assert.Less(t, len(with.Instalments), len(without.Instalments))
}

func TestReserveBalanceFromAStatementAndTheTransfersSince(t *testing.T) {
	r := &Reserve{
		anchors: []Anchor{{On: day(2025, 6, 30), BalanceMinor: 100_000_00}},
		moves: []reserveMove{
			{on: day(2025, 3, 1), amount: 80_000_00},
			{on: day(2025, 7, 15), amount: 20_000_00},
			{on: day(2025, 8, 1), amount: -150_000_00},
		},
	}
	assert.Equal(t, int64(80_000_00), r.BalanceOn(day(2025, 4, 1)))
	assert.Equal(t, int64(100_000_00), r.BalanceOn(day(2025, 6, 30)), "the statement replaces the transfers before it")
	assert.Equal(t, int64(120_000_00), r.BalanceOn(day(2025, 7, 31)))
	assert.Zero(t, r.BalanceOn(day(2025, 8, 2)), "never below zero")
}

func TestALaterPaymentDayAfterARefixationKeepsThatMonthsInstalment(t *testing.T) {
	early := mortgage()
	early.PaymentDay = 5
	refix := mortgage()
	refix.ValidFrom, refix.BalanceMinor, refix.PaymentDay = day(2026, 1, 10), 2_950_000_00, 25
	s := Build([]Terms{early, refix}, nil, nil, nil, day(2026, 6, 1))

	var january []time.Time
	for _, in := range s.Instalments {
		if in.DueOn.Year() == 2026 && in.DueOn.Month() == time.January {
			january = append(january, in.DueOn)
		}
	}
	assert.Equal(t, []time.Time{day(2026, 1, 5), day(2026, 1, 25)}, january, "the old terms' 5th and the new terms' 25th")
	assert.Equal(t, int64(2_950_000_00-(16_104_65-12_291_67)), s.BalanceOn(day(2026, 1, 31)))
}

func TestAPaymentTheTermsBalanceAlreadyIncludesStillHasItsInterest(t *testing.T) {
	from := mortgage()
	from.ValidFrom = day(2025, 1, 15)
	s := Build([]Terms{from}, nil, []Payment{{TransactionID: "jan", BookedOn: day(2025, 1, 17), AmountMinor: 16_104_65}}, nil, day(2025, 2, 1))

	assert.Equal(t, int64(12_500_00), s.Splits["jan"].InterestMinor, "not all principal: the interest is an expense")
	assert.Equal(t, int64(3_000_000_00), s.BalanceOn(day(2025, 1, 31)), "the terms' balance is after it already")
}

func TestAnExtraPaymentLowersTheBalanceTheDayItIsMade(t *testing.T) {
	payments := []Payment{
		{TransactionID: "jan", BookedOn: day(2025, 1, 15), AmountMinor: 16_104_65},
		{TransactionID: "extra", BookedOn: day(2025, 2, 1), AmountMinor: 100_000_00},
		{TransactionID: "feb", BookedOn: day(2025, 2, 15), AmountMinor: 16_104_65},
	}
	s := Build([]Terms{mortgage()}, nil, payments, nil, day(2025, 3, 1))

	afterJanuary := s.Instalments[0].BalanceMinor
	assert.Equal(t, afterJanuary, s.BalanceOn(day(2025, 1, 31)))
	assert.Equal(t, afterJanuary-100_000_00, s.BalanceOn(day(2025, 2, 2)))
}

func TestAPaymentBookedBeforeAStatementBalanceIsAlreadyInIt(t *testing.T) {
	payments := []Payment{
		{TransactionID: "jan", BookedOn: day(2025, 1, 15), AmountMinor: 16_104_65},
		{TransactionID: "extra", BookedOn: day(2025, 2, 5), AmountMinor: 100_000_00},
		{TransactionID: "feb", BookedOn: day(2025, 2, 15), AmountMinor: 16_104_65},
	}
	anchor := Anchor{On: day(2025, 2, 10), BalanceMinor: 2_800_000_00}
	s := Build([]Terms{mortgage()}, []Anchor{anchor}, payments, nil, day(2025, 3, 1))

	feb := s.Instalments[1]
	interest := int64(11_666_67)
	assert.Equal(t, interest, feb.InterestMinor, "on the statement's balance")
	assert.Equal(t, int64(2_800_000_00)-(16_104_65-interest), s.BalanceOn(day(2025, 2, 15)), "the extra payment is not taken off twice")
}

func TestAnExtraPaymentAfterTheTermsStartLowersTheirBalance(t *testing.T) {
	from := mortgage()
	from.ValidFrom = day(2025, 1, 15)
	payments := []Payment{
		{TransactionID: "jan", BookedOn: day(2025, 1, 15), AmountMinor: 16_104_65},
		{TransactionID: "extra", BookedOn: day(2025, 1, 20), AmountMinor: 100_000_00},
	}
	s := Build([]Terms{from}, nil, payments, nil, day(2025, 2, 1))

	assert.Equal(t, int64(2_900_000_00), s.BalanceOn(day(2025, 1, 31)))
	assert.Positive(t, s.Splits["jan"].InterestMinor, "the instalment, not the extra payment, carries the interest")
	assert.Zero(t, s.Splits["extra"].InterestMinor)
}

func TestPaymentsAroundARefixationGoToTheInstalmentsTheScheduleHas(t *testing.T) {
	old := mortgage()
	refix := mortgage()
	refix.ValidFrom, refix.BalanceMinor, refix.PaymentDay = day(2026, 1, 15), 2_950_000_00, 25
	payments := []Payment{
		{TransactionID: "old", BookedOn: day(2026, 1, 15), AmountMinor: 16_104_65},
		{TransactionID: "new", BookedOn: day(2026, 1, 25), AmountMinor: 16_104_65},
	}
	s := Build([]Terms{old, refix}, nil, payments, nil, day(2026, 2, 1))

	assert.Equal(t, int64(12_291_67), s.Splits["new"].InterestMinor)
	assert.Equal(t, int64(2_946_187_02), s.BalanceOn(day(2026, 1, 25)))
	late := Build([]Terms{func() Terms { t := mortgage(); t.PaymentDay = 5; return t }(), func() Terms {
		t := mortgage()
		t.ValidFrom, t.BalanceMinor, t.PaymentDay = day(2026, 1, 6), 2_950_000_00, 25
		return t
	}()}, nil, []Payment{{TransactionID: "jan5", BookedOn: day(2026, 1, 7), AmountMinor: 16_104_65}}, nil, day(2026, 2, 1))
	for _, in := range late.Instalments {
		if in.DueOn.Equal(day(2026, 1, 5)) {
			assert.True(t, in.Paid, "booked two days late, it paid the 5th")
		}
	}
	assert.Positive(t, late.Splits["jan5"].InterestMinor)
}

func TestAFiftyYearLoanRunsToItsMaturity(t *testing.T) {
	long := mortgage()
	long.ValidFrom, long.MaturityOn = day(2025, 1, 15), day(2075, 1, 15)
	long.PaymentMinor = Annuity(long.BalanceMinor, long.AnnualRate, 600)
	s := Build([]Terms{long}, nil, nil, nil, day(2025, 2, 1))

	assert.Len(t, s.Instalments, 600)
	assert.Zero(t, s.BalanceOn(day(2076, 1, 1)))
}

func TestARepaidLoanStopsEvenWithTermsEnteredForLater(t *testing.T) {
	refix := mortgage()
	refix.ValidFrom = day(2026, 1, 1)
	s := Build([]Terms{mortgage(), refix}, nil, []Payment{{TransactionID: "all", BookedOn: day(2025, 1, 15), AmountMinor: 3_012_500_00}},
		nil, day(2025, 2, 1))

	require.NotEmpty(t, s.Instalments)
	assert.Zero(t, s.Instalments[len(s.Instalments)-1].BalanceMinor)
	assert.Equal(t, day(2025, 1, 15), s.Instalments[len(s.Instalments)-1].DueOn)
}

func TestAnExtraPaymentNearASkippedInstalmentStillLowersTheBalance(t *testing.T) {
	refix := mortgage()
	refix.ValidFrom, refix.BalanceMinor, refix.PaymentDay = day(2026, 1, 12), 2_950_000_00, 10
	s := Build([]Terms{mortgage(), refix}, nil, []Payment{{TransactionID: "extra", BookedOn: day(2026, 1, 14), AmountMinor: 100_000_00}},
		nil, day(2026, 3, 1))

	assert.Equal(t, int64(2_850_000_00), s.BalanceOn(day(2026, 1, 14)))
}

func TestAPaymentAfterTheTermsStartIsDatedOnItsOwnDay(t *testing.T) {
	from := mortgage()
	from.ValidFrom = day(2025, 1, 17)
	payments := []Payment{
		{TransactionID: "jan", BookedOn: day(2025, 1, 15), AmountMinor: 16_104_65},
		{TransactionID: "extra", BookedOn: day(2025, 1, 20), AmountMinor: 100_000_00},
	}
	s := Build([]Terms{from}, nil, payments, nil, day(2025, 2, 1))

	assert.Equal(t, int64(3_000_000_00), s.BalanceOn(day(2025, 1, 19)))
	assert.Equal(t, int64(2_900_000_00), s.BalanceOn(day(2025, 1, 20)))
}

func TestWhatIsLeftAtMaturityIsRepaid(t *testing.T) {
	short := mortgage()
	short.MaturityOn, short.PaymentMinor = day(2054, 12, 20), 16_104_00
	s := Build([]Terms{short}, nil, nil, nil, day(2025, 2, 1))

	assert.Zero(t, s.BalanceOn(day(2060, 1, 1)))
	last := s.Instalments[len(s.Instalments)-1]
	assert.False(t, last.DueOn.Before(day(2054, 12, 20)), "the first due day on or after maturity")
}

func TestALatePaymentAfterAStatementStillLowersIt(t *testing.T) {
	end := mortgage()
	end.PaymentDay = 31
	s := Build([]Terms{end}, []Anchor{{On: day(2025, 2, 1), BalanceMinor: 3_000_000_00}},
		[]Payment{{TransactionID: "jan", BookedOn: day(2025, 2, 2), AmountMinor: 16_104_65}}, nil, day(2025, 2, 20))

	assert.Equal(t, int64(3_000_000_00-3_604_65), s.BalanceOn(day(2025, 2, 3)))
}

func TestALoanMaturingBeforeItsFirstDueDayStillSplitsItsPayment(t *testing.T) {
	short := mortgage()
	short.ValidFrom, short.MaturityOn, short.BalanceMinor = day(2025, 1, 1), day(2025, 1, 10), 100_000_00
	s := Build([]Terms{short}, nil, []Payment{{TransactionID: "all", BookedOn: day(2025, 1, 15), AmountMinor: 100_416_67}}, nil, day(2025, 2, 1))

	assert.Contains(t, s.Splits, "all")
	assert.Zero(t, s.BalanceOn(day(2025, 2, 1)))
}

func TestALateInstalmentsRowShowsTheBalanceAfterIt(t *testing.T) {
	end := mortgage()
	end.PaymentDay = 31
	s := Build([]Terms{end}, nil, []Payment{{TransactionID: "jan", BookedOn: day(2025, 2, 2), AmountMinor: 16_104_65}}, nil, day(2025, 2, 20))

	assert.Equal(t, int64(3_000_000_00-3_604_65), s.Instalments[0].BalanceMinor)
	assert.Equal(t, int64(3_000_000_00), s.BalanceOn(day(2025, 2, 1)), "the money leaves on the booking day")
}

func TestALateRegularInstalmentKeepsItsInterestWhenTermsStartInBetween(t *testing.T) {
	from := mortgage()
	from.ValidFrom = day(2025, 1, 16)
	s := Build([]Terms{from}, nil, []Payment{{TransactionID: "jan", BookedOn: day(2025, 1, 17), AmountMinor: 16_104_65}}, nil, day(2025, 2, 1))

	assert.Positive(t, s.Splits["jan"].InterestMinor)
	assert.Equal(t, int64(3_000_000_00), s.BalanceOn(day(2025, 1, 31)))
}

func TestARepaidLoanStaysRepaidThoughLaterPaymentsExist(t *testing.T) {
	refix := mortgage()
	refix.ValidFrom, refix.BalanceMinor = day(2026, 1, 1), 2_950_000_00
	payments := []Payment{
		{TransactionID: "all", BookedOn: day(2025, 1, 15), AmountMinor: 3_012_500_00},
		{TransactionID: "stray", BookedOn: day(2026, 3, 15), AmountMinor: 100_00},
	}
	s := Build([]Terms{mortgage(), refix}, nil, payments, nil, day(2026, 4, 1))

	assert.Zero(t, s.BalanceOn(day(2026, 2, 1)))
}
