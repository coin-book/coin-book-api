// Package networth adds up what the user owns and owes on any day: account balances,
// positions at market prices, assets valued at dates and loans by their schedule (ADR 211).
package networth

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/investment"
	"coin-book-api/internal/domain/loan"
	"coin-book-api/internal/domain/spending"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

// expenseMonths is how many complete months the average spending behind the reserve covers.
const expenseMonths = 12

// Class groups accounts the way the overview shows them.
type Class string

const (
	ClassCash        Class = "cash"
	ClassSavings     Class = "savings"
	ClassInvestments Class = "investments"
	ClassP2P         Class = "p2p"
	ClassPension     Class = "pension"
	ClassProperty    Class = "property"
	ClassOther       Class = "other"
	ClassMortgage    Class = "mortgage"
	ClassLoan        Class = "loan"
)

func ClassOf(k account.Kind) Class {
	switch k {
	case account.Savings, account.Pocket, account.Fund, account.LoanReserve:
		return ClassSavings
	case account.Broker:
		return ClassInvestments
	case account.P2P:
		return ClassP2P
	case account.Pension:
		return ClassPension
	case account.Property:
		return ClassProperty
	case account.OtherAsset:
		return ClassOther
	case account.Mortgage:
		return ClassMortgage
	case account.Loan:
		return ClassLoan
	default:
		return ClassCash
	}
}

// PriceHistory prices positions and converts currencies on past days.
type PriceHistory interface {
	ValuesOn(ctx context.Context, days []time.Time) (map[time.Time]investment.HoldingsValue, error)
	RatesOn(ctx context.Context, days []time.Time, currencies []money.Currency) (investment.Rates, error)
}

// Loans value loans by their schedules, which are split again once a balance is corrected by hand.
type Loans interface {
	Schedules(ctx context.Context) (map[uuid.UUID]*loan.Schedule, error)
	Reserves(ctx context.Context) (map[uuid.UUID]*loan.Reserve, error)
	Reconcile(ctx context.Context, q db.Querier) error
}

type Service struct {
	repo     *Repo
	prices   PriceHistory
	loans    Loans
	spending *spending.Repo
	clock    clock.Clock
}

func NewService(repo *Repo, prices PriceHistory, loans Loans, spendingRepo *spending.Repo, c clock.Clock) *Service {
	return &Service{repo: repo, prices: prices, loans: loans, spending: spendingRepo, clock: c}
}

type AccountValue struct {
	Account   account.Account
	Class     Class
	Liquidity account.Liquidity
	// ValueMinor is in the account's currency and negative for a liability; a broker account
	// adds its positions, which have no single currency, only in ValueCZKMinor.
	ValueMinor int64
	// ValueCZKMinor is nil without an exchange rate.
	ValueCZKMinor *int64
	// PositionsCZKMinor is the part of ValueCZKMinor a broker account holds in positions.
	PositionsCZKMinor int64
	// ValuedOn is the day of the value an asset valued by hand carries; nil for the others.
	ValuedOn *time.Time
	// Unvalued: an asset never valued, or a loan without terms or a balance.
	Unvalued bool
}

// Point is net worth at the end of a day, in CZK.
type Point struct {
	On               time.Time
	AssetsCZKMinor   int64
	LiabilitiesMinor int64
	Accounts         []AccountValue
	// UnvaluedPositions are positions without a price or a rate that day.
	UnvaluedPositions int
	// Unconverted are currencies held without a rate.
	Unconverted []money.Currency
}

func (p Point) NetCZKMinor() int64 { return p.AssetsCZKMinor - p.LiabilitiesMinor }

// valuesOn values every account that is not archived on each day.
func (s *Service) valuesOn(ctx context.Context, days []time.Time) ([]Point, error) {
	accounts, err := s.repo.accounts(ctx)
	if err != nil {
		return nil, err
	}
	ledger, err := s.repo.ledgerBalances(ctx, days)
	if err != nil {
		return nil, err
	}
	dated, err := s.repo.datedValues(ctx)
	if err != nil {
		return nil, err
	}
	holdings, err := s.prices.ValuesOn(ctx, days)
	if err != nil {
		return nil, err
	}
	schedules, err := s.loans.Schedules(ctx)
	if err != nil {
		return nil, err
	}
	reserves, err := s.loans.Reserves(ctx)
	if err != nil {
		return nil, err
	}
	var currencies []money.Currency
	for _, a := range accounts {
		if !slices.Contains(currencies, a.Currency) {
			currencies = append(currencies, a.Currency)
		}
	}
	rates, err := s.prices.RatesOn(ctx, days, currencies)
	if err != nil {
		return nil, err
	}

	points := make([]Point, 0, len(days))
	for _, day := range days {
		p := Point{On: day, Unconverted: []money.Currency{}, UnvaluedPositions: holdings[day].Unvalued}
		for _, a := range accounts {
			v := AccountValue{Account: a, Class: ClassOf(a.Kind), Liquidity: a.Kind.Liquidity()}
			switch a.Kind.Valuation() {
			case account.ByLedger, account.ByMarket:
				v.ValueMinor = ledger[a.ID][day]
			case account.BySnapshot:
				if dv, ok := latest(dated[a.ID], day); ok {
					v.ValueMinor, v.ValuedOn = dv.value, &dv.on
				} else {
					v.Unvalued = true
				}
			case account.BySchedule:
				owed, ok := owedOn(schedules[a.ID], dated[a.ID], day)
				v.ValueMinor, v.Unvalued = -owed, !ok
			case account.ByFlows:
				v.ValueMinor = reserves[a.ID].BalanceOn(day)
			}
			if inCZK, ok := rates.ToCZK(v.ValueMinor, a.Currency, day); ok {
				if a.Kind.Valuation() == account.ByMarket {
					v.PositionsCZKMinor = holdings[day].ByAccount[a.ID]
					inCZK += v.PositionsCZKMinor
				}
				v.ValueCZKMinor = &inCZK
				if inCZK >= 0 {
					p.AssetsCZKMinor += inCZK
				} else {
					p.LiabilitiesMinor -= inCZK
				}
			} else if v.ValueMinor != 0 && !slices.Contains(p.Unconverted, a.Currency) {
				p.Unconverted = append(p.Unconverted, a.Currency)
			}
			p.Accounts = append(p.Accounts, v)
		}
		points = append(points, p)
	}
	return points, nil
}

// latest is the value in force at the end of day; a valuation entered by hand beats an
// imported snapshot of the same day.
func latest(values []datedValue, day time.Time) (datedValue, bool) {
	var found datedValue
	ok := false
	for _, v := range values {
		if v.on.After(day) {
			break
		}
		if ok && v.on.Equal(found.on) && v.imported {
			continue
		}
		found, ok = v, true
	}
	return found, ok
}

// owedOn takes the schedule's balance; a loan without terms falls back on balances entered by hand.
func owedOn(schedule *loan.Schedule, dated []datedValue, day time.Time) (int64, bool) {
	if schedule != nil && len(schedule.Instalments) > 0 {
		return schedule.BalanceOn(day), true
	}
	if v, ok := latest(dated, day); ok {
		return v.value, true
	}
	return 0, false
}

// Summary is net worth today, with the reserve it gives against the average spending.
type Summary struct {
	Point
	ByClass     map[Class]int64
	ByLiquidity map[account.Liquidity]int64
	// MonthlyExpensesCZKMinor averages the last complete months' spending, without transfers
	// and repayments; zero when there was none.
	MonthlyExpensesCZKMinor int64
	// ReserveMonths is the immediately available money in months of that spending; nil without spending.
	ReserveMonths *decimal.Decimal
}

func (s *Service) Summary(ctx context.Context) (*Summary, error) {
	today := clock.Today(s.clock)
	points, err := s.valuesOn(ctx, []time.Time{today})
	if err != nil {
		return nil, err
	}
	sum := &Summary{Point: points[0], ByClass: map[Class]int64{}, ByLiquidity: map[account.Liquidity]int64{}}
	for _, v := range sum.Accounts {
		if v.ValueCZKMinor == nil {
			continue
		}
		sum.ByClass[v.Class] += *v.ValueCZKMinor
		if v.Liquidity != "" {
			sum.ByLiquidity[v.Liquidity] += *v.ValueCZKMinor
		}
	}
	if sum.MonthlyExpensesCZKMinor, err = s.monthlyExpenses(ctx, today); err != nil {
		return nil, err
	}
	if sum.MonthlyExpensesCZKMinor > 0 {
		months := decimal.NewFromInt(sum.ByLiquidity[account.Immediate]).Div(decimal.NewFromInt(sum.MonthlyExpensesCZKMinor)).Round(1)
		sum.ReserveMonths = &months
	}
	return sum, nil
}

func (s *Service) monthlyExpenses(ctx context.Context, today time.Time) (int64, error) {
	to := spending.MonthStart(today)
	totals, err := s.spending.Totals(ctx, to.AddDate(0, -expenseMonths, 0), to, money.CZK)
	if err != nil {
		return 0, err
	}
	var spent int64
	months := map[time.Time]bool{}
	for _, t := range totals {
		if t.Kind == categorization.Expense {
			spent -= t.AmountMinor
			months[t.Month] = true
		}
	}
	if len(months) == 0 || spent <= 0 {
		return 0, nil
	}
	return spent / int64(len(months)), nil
}

// MonthPoint is net worth at a month's end (today for the current month) and how it changed:
// what the month saved from income, and the rest, which is returns and revaluation.
type MonthPoint struct {
	Point
	ChangeCZKMinor int64
	SavedCZKMinor  int64
}

func (m MonthPoint) OtherChangeCZKMinor() int64 { return m.ChangeCZKMinor - m.SavedCZKMinor }

// History is the last months' net worth, oldest first.
func (s *Service) History(ctx context.Context, months int) ([]MonthPoint, error) {
	today := clock.Today(s.clock)
	current := spending.MonthStart(today)
	days := make([]time.Time, 0, months+1)
	// One more month end before the first, to tell the first month's change.
	for i := months; i >= 1; i-- {
		days = append(days, current.AddDate(0, -i+1, -1))
	}
	days = append(days, today)
	points, err := s.valuesOn(ctx, days)
	if err != nil {
		return nil, err
	}
	totals, err := s.spending.Totals(ctx, current.AddDate(0, -(months-1), 0), current.AddDate(0, 1, 0), money.CZK)
	if err != nil {
		return nil, err
	}
	saved := map[time.Time]int64{}
	for _, t := range totals {
		if t.Kind == categorization.Income || t.Kind == categorization.Expense {
			saved[t.Month] += t.AmountMinor
		}
	}
	out := make([]MonthPoint, 0, months)
	for i := 1; i < len(points); i++ {
		p := points[i]
		out = append(out, MonthPoint{Point: p, ChangeCZKMinor: p.NetCZKMinor() - points[i-1].NetCZKMinor(),
			SavedCZKMinor: saved[spending.MonthStart(p.On)]})
	}
	return out, nil
}
