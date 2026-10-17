package reports

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/report"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

type Handler struct {
	service *report.Service
}

func NewHandler(service *report.Service) *Handler { return &Handler{service: service} }

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

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeReportsRead, Operation: huma.Operation{
		OperationID: "get-monthly-report", Method: http.MethodGet, Path: "/api/v1/reports/monthly/{month}",
		Tags: []string{"Reports"}, Summary: "Income and spending by category for a month, with a six-month trend",
		Description: "Transfers between own accounts are excluded.", Errors: []int{422},
	}}, h.monthly)
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
