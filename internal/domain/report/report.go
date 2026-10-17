package report

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/spending"
	"coin-book-api/internal/platform/money"
)

const trendMonths = 6

type CategoryTotal struct {
	// CategoryID is nil for the not-yet-categorised share.
	CategoryID  *uuid.UUID
	AmountMinor int64
}

type MonthTotals struct {
	Month          time.Time
	IncomeMinor    int64
	ExpenseMinor   int64
	FinancialMinor int64
}

type Monthly struct {
	MonthTotals
	Currency money.Currency
	// Expenses are positive amounts per main category, largest first: the month ribbon.
	Expenses []CategoryTotal
	Income   []CategoryTotal
	Trend    []MonthTotals
	// SetAside is what transfers moved into saving accounts this month, net per account.
	SetAside []spending.AccountFlow
}

type Service struct {
	spending *spending.Repo
}

func NewService(spendingRepo *spending.Repo) *Service { return &Service{spending: spendingRepo} }

func (s *Service) Monthly(ctx context.Context, month time.Time, currency money.Currency) (*Monthly, error) {
	month = spending.MonthStart(month)
	from := month.AddDate(0, -(trendMonths - 1), 0)
	totals, err := s.spending.Totals(ctx, from, month.AddDate(0, 1, 0), currency)
	if err != nil {
		return nil, err
	}

	byMonth := map[time.Time]*MonthTotals{}
	for i := range trendMonths {
		m := from.AddDate(0, i, 0)
		byMonth[m] = &MonthTotals{Month: m}
	}
	expenses := map[uuid.UUID]int64{}
	income := map[uuid.UUID]int64{}
	var uncategorizedExpense, uncategorizedIncome int64
	for _, t := range totals {
		mt := byMonth[t.Month]
		if mt == nil {
			continue
		}
		switch t.Kind {
		case categorization.Income:
			mt.IncomeMinor += t.AmountMinor
		case categorization.Expense:
			mt.ExpenseMinor -= t.AmountMinor
		case categorization.Financial:
			mt.FinancialMinor -= t.AmountMinor
		case categorization.Transfer:
		}
		if !t.Month.Equal(month) {
			continue
		}
		switch {
		case t.Kind == categorization.Expense && t.MainCategoryID == nil:
			uncategorizedExpense -= t.AmountMinor
		case t.Kind == categorization.Expense:
			expenses[*t.MainCategoryID] -= t.AmountMinor
		case t.Kind == categorization.Income && t.MainCategoryID == nil:
			uncategorizedIncome += t.AmountMinor
		case t.Kind == categorization.Income:
			income[*t.MainCategoryID] += t.AmountMinor
		}
	}

	setAside, err := s.spending.SetAside(ctx, month, month.AddDate(0, 1, 0), currency)
	if err != nil {
		return nil, err
	}
	out := &Monthly{MonthTotals: *byMonth[month], Currency: currency, SetAside: setAside}
	out.Expenses = ranked(expenses, uncategorizedExpense)
	out.Income = ranked(income, uncategorizedIncome)
	for i := range trendMonths {
		out.Trend = append(out.Trend, *byMonth[from.AddDate(0, i, 0)])
	}
	return out, nil
}

func ranked(byCategory map[uuid.UUID]int64, uncategorized int64) []CategoryTotal {
	out := make([]CategoryTotal, 0, len(byCategory)+1)
	for id, amount := range byCategory {
		if amount > 0 {
			out = append(out, CategoryTotal{CategoryID: &id, AmountMinor: amount})
		}
	}
	if uncategorized > 0 {
		out = append(out, CategoryTotal{AmountMinor: uncategorized})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AmountMinor > out[j].AmountMinor })
	return out
}
