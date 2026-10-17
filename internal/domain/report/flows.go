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

type NodeKind string

const (
	// NodeIncome is an income category; NodeExpense and NodeFinancial are spending
	// categories; NodeAccount an own account; NodeSaving a saving account money went into.
	NodeIncome    NodeKind = "income"
	NodeAccount   NodeKind = "account"
	NodeExpense   NodeKind = "expense"
	NodeFinancial NodeKind = "financial"
	NodeSaving    NodeKind = "saving"
)

type Node struct {
	Kind NodeKind
	// CategoryID is set for category nodes; nil for money not categorised yet.
	CategoryID *uuid.UUID
	// AccountID is set for account and saving nodes.
	AccountID *uuid.UUID
}

func (n Node) Key() string {
	switch {
	case n.CategoryID != nil:
		return string(n.Kind) + ":" + n.CategoryID.String()
	case n.AccountID != nil:
		return string(n.Kind) + ":" + n.AccountID.String()
	default:
		return string(n.Kind) + ":uncategorized"
	}
}

type Link struct {
	Source      Node
	Target      Node
	AmountMinor int64
}

// Flows is the month's money as a graph: income categories into accounts, accounts
// into spending categories and saving accounts. Links are positive and ordered by size.
type Flows struct {
	Month    time.Time
	Currency money.Currency
	Links    []Link
}

func (s *Service) Flows(ctx context.Context, month time.Time, currency money.Currency) (*Flows, error) {
	month = spending.MonthStart(month)
	to := month.AddDate(0, 1, 0)
	totals, err := s.spending.ByAccount(ctx, month, to, currency)
	if err != nil {
		return nil, err
	}
	savings, err := s.spending.SavingTransfers(ctx, month, to, currency)
	if err != nil {
		return nil, err
	}
	out := &Flows{Month: month, Currency: currency}
	for _, t := range totals {
		account := Node{Kind: NodeAccount, AccountID: ptr(t.AccountID)}
		switch {
		case t.Kind == categorization.Income && t.AmountMinor > 0:
			out.Links = append(out.Links, Link{Source: Node{Kind: NodeIncome, CategoryID: t.MainCategoryID}, Target: account, AmountMinor: t.AmountMinor})
		case t.Kind == categorization.Expense && t.AmountMinor < 0:
			out.Links = append(out.Links, Link{Source: account, Target: Node{Kind: NodeExpense, CategoryID: t.MainCategoryID}, AmountMinor: -t.AmountMinor})
		case t.Kind == categorization.Financial && t.AmountMinor < 0:
			out.Links = append(out.Links, Link{Source: account, Target: Node{Kind: NodeFinancial, CategoryID: t.MainCategoryID}, AmountMinor: -t.AmountMinor})
		case t.Kind == categorization.Transfer:
		}
	}
	for _, f := range savings {
		out.Links = append(out.Links, Link{Source: Node{Kind: NodeAccount, AccountID: ptr(f.FromAccountID)},
			Target: Node{Kind: NodeSaving, AccountID: ptr(f.ToAccountID)}, AmountMinor: f.AmountMinor})
	}
	sort.SliceStable(out.Links, func(i, j int) bool { return out.Links[i].AmountMinor > out.Links[j].AmountMinor })
	return out, nil
}

func ptr(id uuid.UUID) *uuid.UUID { return &id }

// CategoryMonth is one month's spending per main expense category.
type CategoryMonth struct {
	Month  time.Time
	Totals []CategoryTotal
}

// Categories gives the expense split per main category for the months ending with
// month, oldest first; months without spending are present with no totals.
func (s *Service) Categories(ctx context.Context, month time.Time, months int, currency money.Currency) ([]CategoryMonth, error) {
	month = spending.MonthStart(month)
	from := month.AddDate(0, -(months - 1), 0)
	totals, err := s.spending.Totals(ctx, from, month.AddDate(0, 1, 0), currency)
	if err != nil {
		return nil, err
	}
	byMonth := map[time.Time]map[uuid.UUID]int64{}
	uncategorized := map[time.Time]int64{}
	for _, t := range totals {
		if t.Kind != categorization.Expense {
			continue
		}
		if byMonth[t.Month] == nil {
			byMonth[t.Month] = map[uuid.UUID]int64{}
		}
		if t.MainCategoryID == nil {
			uncategorized[t.Month] -= t.AmountMinor
		} else {
			byMonth[t.Month][*t.MainCategoryID] -= t.AmountMinor
		}
	}
	out := make([]CategoryMonth, 0, months)
	for i := range months {
		m := from.AddDate(0, i, 0)
		out = append(out, CategoryMonth{Month: m, Totals: ranked(byMonth[m], uncategorized[m])})
	}
	return out, nil
}
