package reports

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/investment"
	"coin-book-api/internal/domain/report"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

// AccountBalances are the signed-in user's accounts with their balances.
type AccountBalances interface {
	ListWithBalances(ctx context.Context) ([]account.WithBalance, error)
}

type NetWorthCalculator interface {
	NetWorth(ctx context.Context, balances []money.Amount) (*investment.NetWorth, error)
}

type Handler struct {
	service  *report.Service
	accounts AccountBalances
	netWorth NetWorthCalculator
}

func NewHandler(service *report.Service, accounts AccountBalances, netWorth NetWorthCalculator) *Handler {
	return &Handler{service: service, accounts: accounts, netWorth: netWorth}
}

type NetWorth struct {
	TotalCZK          money.Amount `json:"total_czk"`
	CashCZK           money.Amount `json:"cash_czk" doc:"Balances of the accounts that are not archived, at the latest ČNB rates."`
	InvestmentsCZK    money.Amount `json:"investments_czk" doc:"Market value of the open positions."`
	Unconverted       []string     `json:"unconverted" doc:"Currencies held without a ČNB rate yet; their balances are left out."`
	UnvaluedPositions int          `json:"unvalued_positions" doc:"Positions without a price or rate, left out."`
}

type netWorthOutput struct {
	Body NetWorth
}

type monthInput struct {
	Month    string `path:"month" pattern:"^[0-9]{4}-[0-9]{2}$" example:"2026-10"`
	Currency string `query:"currency" default:"CZK" pattern:"^[A-Z]{3}$"`
}

type CategoryTotal struct {
	CategoryID  *uuid.UUID `json:"category_id,omitempty" doc:"Absent for the not-yet-categorised share."`
	AmountMinor int64      `json:"amount_minor"`
}

type MonthTotals struct {
	Month          string `json:"month"`
	IncomeMinor    int64  `json:"income_minor"`
	ExpenseMinor   int64  `json:"expense_minor" doc:"Positive: money spent."`
	FinancialMinor int64  `json:"financial_minor" doc:"Saving and loan repayments: set aside, not spent (F-KAT-2)."`
}

type MonthlyReport struct {
	MonthTotals
	Currency string          `json:"currency"`
	Expenses []CategoryTotal `json:"expenses" doc:"Per main category, largest first."`
	Income   []CategoryTotal `json:"income"`
	Trend    []MonthTotals   `json:"trend" doc:"The last six months, oldest first."`
	SetAside []AccountFlow   `json:"set_aside" doc:"Transfers into saving accounts (savings, pocket, fund, broker), net per account."`
}

type AccountFlow struct {
	AccountID   uuid.UUID `json:"account_id"`
	AmountMinor int64     `json:"amount_minor" doc:"Positive: put aside; negative: taken back."`
}

type monthlyReportOutput struct {
	Body MonthlyReport
}

type FlowNode struct {
	Key        string     `json:"key" doc:"Stable id to reference from links."`
	Kind       string     `json:"kind" enum:"income,account,expense,financial,saving" doc:"income/expense/financial: a main category (category_id absent = not yet categorised); account: an own account; saving: a saving account money went into."`
	CategoryID *uuid.UUID `json:"category_id,omitempty"`
	AccountID  *uuid.UUID `json:"account_id,omitempty"`
}

type FlowLink struct {
	Source      string `json:"source" doc:"Node key."`
	Target      string `json:"target" doc:"Node key."`
	AmountMinor int64  `json:"amount_minor" doc:"Always positive."`
}

type FlowsReport struct {
	Month    string     `json:"month"`
	Currency string     `json:"currency"`
	Nodes    []FlowNode `json:"nodes"`
	Links    []FlowLink `json:"links" doc:"Largest first. Income categories flow into accounts; accounts flow into spending categories and saving accounts."`
}

type flowsOutput struct {
	Body FlowsReport
}

type categoriesInput struct {
	To       string `query:"to" pattern:"^[0-9]{4}-[0-9]{2}$" required:"true" doc:"Last month of the range."`
	Months   int    `query:"months" minimum:"1" maximum:"36" default:"12"`
	Currency string `query:"currency" default:"CZK" pattern:"^[A-Z]{3}$"`
}

type CategoryMonth struct {
	Month    string          `json:"month"`
	Expenses []CategoryTotal `json:"expenses" doc:"Per main category, largest first; empty for a month without spending."`
}

type CategoriesReport struct {
	Currency string          `json:"currency"`
	Months   []CategoryMonth `json:"months" doc:"Oldest first."`
}

type categoriesOutput struct {
	Body CategoriesReport
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeReportsRead, Operation: huma.Operation{
		OperationID: "get-monthly-report", Method: http.MethodGet, Path: "/api/v1/reports/monthly/{month}",
		Tags: []string{"Reports"}, Summary: "Income and spending by category for a month, with a twelve-month trend",
		Description: "Transfers between own accounts are excluded.", Errors: []int{422},
	}}, h.monthly)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeReportsRead, Operation: huma.Operation{
		OperationID: "get-flows-report", Method: http.MethodGet, Path: "/api/v1/reports/flows/{month}",
		Tags: []string{"Reports"}, Summary: "Where the month's money came from and went, as a flow graph",
		Description: "Income categories flow into accounts; accounts flow into spending categories and saving accounts. " +
			"Transfers between own accounts are not spending, only those into saving accounts appear, as set-aside money.",
		Errors: []int{422},
	}}, h.flows)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeReportsRead, Operation: huma.Operation{
		OperationID: "get-net-worth", Method: http.MethodGet, Path: "/api/v1/reports/net-worth",
		Tags: []string{"Reports"}, Summary: "What the user owns today: account balances and investments, in CZK",
	}}, h.netWorthReport)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeReportsRead, Operation: huma.Operation{
		OperationID: "get-categories-report", Method: http.MethodGet, Path: "/api/v1/reports/categories",
		Tags: []string{"Reports"}, Summary: "Spending per main category for each of the last months", Errors: []int{422},
	}}, h.categories)
}

func (h *Handler) flows(ctx context.Context, in *monthInput) (*flowsOutput, error) {
	month, err := dto.ParseMonth(in.Month)
	if err != nil {
		return nil, err
	}
	currency, err := dto.Currency(in.Currency)
	if err != nil {
		return nil, err
	}
	f, err := h.service.Flows(ctx, month, currency)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := FlowsReport{Month: dto.Month(f.Month), Currency: string(f.Currency), Nodes: []FlowNode{}, Links: []FlowLink{}}
	seen := map[string]bool{}
	add := func(n report.Node) string {
		key := n.Key()
		if !seen[key] {
			seen[key] = true
			out.Nodes = append(out.Nodes, FlowNode{Key: key, Kind: string(n.Kind), CategoryID: n.CategoryID, AccountID: n.AccountID})
		}
		return key
	}
	for _, l := range f.Links {
		out.Links = append(out.Links, FlowLink{Source: add(l.Source), Target: add(l.Target), AmountMinor: l.AmountMinor})
	}
	httpapi.CountResults(ctx, len(out.Links))
	return &flowsOutput{Body: out}, nil
}

func (h *Handler) categories(ctx context.Context, in *categoriesInput) (*categoriesOutput, error) {
	to, err := dto.ParseMonth(in.To)
	if err != nil {
		return nil, err
	}
	currency, err := dto.Currency(in.Currency)
	if err != nil {
		return nil, err
	}
	months, err := h.service.Categories(ctx, to, in.Months, currency)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := CategoriesReport{Currency: string(currency), Months: make([]CategoryMonth, 0, len(months))}
	for _, m := range months {
		out.Months = append(out.Months, CategoryMonth{Month: dto.Month(m.Month), Expenses: toCategoryTotals(m.Totals)})
	}
	httpapi.CountResults(ctx, len(out.Months))
	return &categoriesOutput{Body: out}, nil
}

func (h *Handler) monthly(ctx context.Context, in *monthInput) (*monthlyReportOutput, error) {
	month, err := dto.ParseMonth(in.Month)
	if err != nil {
		return nil, err
	}
	currency, err := dto.Currency(in.Currency)
	if err != nil {
		return nil, err
	}
	m, err := h.service.Monthly(ctx, month, currency)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := MonthlyReport{MonthTotals: toTotals(m.MonthTotals), Currency: string(m.Currency),
		Expenses: toCategoryTotals(m.Expenses), Income: toCategoryTotals(m.Income), SetAside: []AccountFlow{}}
	for _, f := range m.SetAside {
		out.SetAside = append(out.SetAside, AccountFlow{AccountID: f.AccountID, AmountMinor: f.AmountMinor})
	}
	for _, t := range m.Trend {
		out.Trend = append(out.Trend, toTotals(t))
	}
	httpapi.CountResults(ctx, 1)
	return &monthlyReportOutput{Body: out}, nil
}

func toTotals(t report.MonthTotals) MonthTotals {
	return MonthTotals{Month: dto.Month(t.Month), IncomeMinor: t.IncomeMinor, ExpenseMinor: t.ExpenseMinor, FinancialMinor: t.FinancialMinor}
}

func toCategoryTotals(in []report.CategoryTotal) []CategoryTotal {
	out := make([]CategoryTotal, len(in))
	for i, c := range in {
		out[i] = CategoryTotal{CategoryID: c.CategoryID, AmountMinor: c.AmountMinor}
	}
	return out
}

func (h *Handler) netWorthReport(ctx context.Context, _ *struct{}) (*netWorthOutput, error) {
	accounts, err := h.accounts.ListWithBalances(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	balances := make([]money.Amount, 0, len(accounts))
	for _, a := range accounts {
		if !a.Archived {
			balances = append(balances, a.Balance)
		}
	}
	n, err := h.netWorth.NetWorth(ctx, balances)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := NetWorth{
		TotalCZK: money.New(n.TotalCZKMinor(), money.CZK), CashCZK: money.New(n.CashCZKMinor, money.CZK),
		InvestmentsCZK: money.New(n.InvestmentsCZKMinor, money.CZK), Unconverted: make([]string, 0, len(n.Unconverted)),
		UnvaluedPositions: n.UnvaluedPositions,
	}
	for _, c := range n.Unconverted {
		out.Unconverted = append(out.Unconverted, string(c))
	}
	return &netWorthOutput{Body: out}, nil
}
