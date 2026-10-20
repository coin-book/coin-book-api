package loan

import (
	"time"

	"github.com/shopspring/decimal"
)

// Repayment is how a balance is paid off from the next instalment on.
type Repayment struct {
	PaymentMinor  int64
	Months        int
	InterestMinor int64
	PaidOffOn     time.Time
}

// Prepayment compares repaying the balance as it is with an extra payment made today that
// either keeps the instalment (the loan ends sooner) or keeps the maturity (it gets lower).
type Prepayment struct {
	ExtraMinor   int64
	Current      Repayment
	KeepPayment  Repayment
	KeepMaturity Repayment
}

// SimulatePrepayment runs the terms in force on day from the balance owed that day; offset is held
// against the loan throughout and bears no interest.
func SimulatePrepayment(t Terms, balance, offset, extra int64, day time.Time) Prepayment {
	first := dueDay(monthOf(day), t.PaymentDay)
	if !first.After(day) {
		first = dueDay(monthOf(day).AddDate(0, 1, 0), t.PaymentDay)
	}
	months := 0
	for due := first; !due.After(t.MaturityOn); due = dueDay(monthOf(due).AddDate(0, 1, 0), t.PaymentDay) {
		months++
	}
	months = max(months, 1)
	reduced := max(0, balance-extra)
	return Prepayment{
		ExtraMinor:   extra,
		Current:      repay(balance, offset, t.AnnualRate, t.PaymentMinor, months, first, t.PaymentDay),
		KeepPayment:  repay(reduced, offset, t.AnnualRate, t.PaymentMinor, months, first, t.PaymentDay),
		KeepMaturity: repay(reduced, offset, t.AnnualRate, Annuity(reduced, t.AnnualRate, months), months, first, t.PaymentDay),
	}
}

// repay pays the balance with the instalment, the last one taking what is left by the last month.
func repay(balance, offset int64, annualRate decimal.Decimal, payment int64, months int, first time.Time, paymentDay int) Repayment {
	r := Repayment{PaymentMinor: payment}
	due := first
	for balance > 0 && r.Months < maxVisits {
		interest := monthlyInterest(max(0, balance-offset), annualRate)
		principal := max(0, min(payment-interest, balance))
		if r.Months+1 >= months {
			principal = balance
		}
		balance -= principal
		r.InterestMinor += interest
		r.Months++
		r.PaidOffOn = due
		due = dueDay(monthOf(due).AddDate(0, 1, 0), paymentDay)
	}
	return r
}

// Annuity is the level monthly instalment that repays the balance in the given months.
func Annuity(balance int64, annualRate decimal.Decimal, months int) int64 {
	if balance <= 0 || months <= 0 {
		return 0
	}
	if annualRate.IsZero() {
		return decimal.NewFromInt(balance).Div(decimal.NewFromInt(int64(months))).Ceil().IntPart()
	}
	i := annualRate.Div(decimal.NewFromInt(100)).Div(monthsPerYear)
	growth := decimal.NewFromInt(1).Add(i).Pow(decimal.NewFromInt(int64(months)))
	return decimal.NewFromInt(balance).Mul(i).Mul(growth).Div(growth.Sub(decimal.NewFromInt(1))).Round(0).IntPart()
}
