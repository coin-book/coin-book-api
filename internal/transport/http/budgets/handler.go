package budgets

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/budget"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

type Handler struct {
	service *budget.Service
}

func NewHandler(service *budget.Service) *Handler { return &Handler{service: service} }

type monthInput struct {
	Month    string `path:"month" pattern:"^[0-9]{4}-[0-9]{2}$" example:"2026-10"`
	Currency string `query:"currency" default:"CZK" pattern:"^[A-Z]{3}$"`
}

type BudgetLine struct {
	CategoryID     *uuid.UUID `json:"category_id,omitempty"`
	BudgetGroupID  *uuid.UUID `json:"budget_group_id,omitempty"`
	PlannedMinor   int64      `json:"planned_minor"`
	SpentMinor     int64      `json:"spent_minor"`
	RemainingMinor int64      `json:"remaining_minor"`
	ProjectedMinor int64      `json:"projected_minor" doc:"Spending by month end at the current pace."`
	Status         string     `json:"status" enum:"on_track,ahead_of_pace,over"`
}

type UnbudgetedCategory struct {
	CategoryID uuid.UUID `json:"category_id"`
	SpentMinor int64     `json:"spent_minor"`
}

type BudgetMonth struct {
	Month          string               `json:"month" example:"2026-10"`
	Currency       string               `json:"currency"`
	Lines          []BudgetLine         `json:"lines"`
	Unbudgeted     []UnbudgetedCategory `json:"unbudgeted" doc:"Main categories with spending but no budget line."`
	PlannedMinor   int64                `json:"planned_minor"`
	SpentMinor     int64                `json:"spent_minor"`
	RemainingMinor int64                `json:"remaining_minor" doc:"What is left to spend this month (F-VYU-3)."`
	Elapsed        float64              `json:"elapsed" doc:"Share of the month gone, 0–1; where the pace marker sits."`
	DaysLeft       int                  `json:"days_left" doc:"Including today."`
	PerDayMinor    int64                `json:"per_day_minor"`
}

type budgetMonthOutput struct {
	Body BudgetMonth
}

type BudgetLineInput struct {
	CategoryID    *uuid.UUID `json:"category_id,omitempty"`
	BudgetGroupID *uuid.UUID `json:"budget_group_id,omitempty"`
	PlannedMinor  int64      `json:"planned_minor" minimum:"0"`
}

type replaceBudgetInput struct {
	Month    string `path:"month" pattern:"^[0-9]{4}-[0-9]{2}$"`
	Currency string `query:"currency" default:"CZK" pattern:"^[A-Z]{3}$"`
	Body     struct {
		Lines []BudgetLineInput `json:"lines" maxItems:"200"`
	}
}

type copyBudgetOutput struct {
	Body struct {
		Copied int `json:"copied"`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeBudgetsRead, Operation: huma.Operation{
		OperationID: "get-budget", Method: http.MethodGet, Path: "/api/v1/budgets/{month}", Tags: []string{"Budgets"},
		Summary: "Plan, actual and remaining per budget line for one month", Errors: []int{422},
	}}, h.get)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "replace-budget", Method: http.MethodPut, Path: "/api/v1/budgets/{month}", Tags: []string{"Budgets"},
		Summary: "Set the month's whole plan", Errors: []int{422},
	}}, h.replace)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "copy-previous-budget", Method: http.MethodPost, Path: "/api/v1/budgets/{month}/copy-previous",
		Tags: []string{"Budgets"}, Summary: "Start an empty month from the previous month's plan", Errors: []int{422},
	}}, h.copyPrevious)
}

func (h *Handler) get(ctx context.Context, in *monthInput) (*budgetMonthOutput, error) {
	month, err := dto.ParseMonth(in.Month)
	if err != nil {
		return nil, err
	}
	currency, err := dto.Currency(in.Currency)
	if err != nil {
		return nil, err
	}
	return h.render(ctx, month, currency)
}

func (h *Handler) replace(ctx context.Context, in *replaceBudgetInput) (*budgetMonthOutput, error) {
	month, err := dto.ParseMonth(in.Month)
	if err != nil {
		return nil, err
	}
	currency, err := dto.Currency(in.Currency)
	if err != nil {
		return nil, err
	}
	lines := make([]budget.Line, len(in.Body.Lines))
	for i, l := range in.Body.Lines {
		lines[i] = budget.Line{CategoryID: l.CategoryID, BudgetGroupID: l.BudgetGroupID, PlannedMinor: l.PlannedMinor}
	}
	err = h.service.Replace(ctx, month, currency, lines)
	switch {
	case errors.Is(err, budget.ErrInvalidLine), errors.Is(err, budget.ErrUnknownTarget):
		return nil, apierr.Unprocessable(apierr.CodeInvalidBudgetLine)
	case errors.Is(err, budget.ErrDuplicate):
		return nil, apierr.Unprocessable(apierr.CodeDuplicateBudgetLine)
	case err != nil:
		return nil, apierr.Internal(ctx, err)
	}
	return h.render(ctx, month, currency)
}

func (h *Handler) copyPrevious(ctx context.Context, in *monthInput) (*copyBudgetOutput, error) {
	month, err := dto.ParseMonth(in.Month)
	if err != nil {
		return nil, err
	}
	currency, err := dto.Currency(in.Currency)
	if err != nil {
		return nil, err
	}
	copied, err := h.service.CopyPrevious(ctx, month, currency)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &copyBudgetOutput{}
	out.Body.Copied = copied
	return out, nil
}

func (h *Handler) render(ctx context.Context, month time.Time, currency money.Currency) (*budgetMonthOutput, error) {
	m, err := h.service.Get(ctx, month, currency)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := BudgetMonth{
		Month: dto.Month(m.Month), Currency: string(m.Currency), PlannedMinor: m.PlannedMinor, SpentMinor: m.SpentMinor,
		RemainingMinor: m.RemainingMinor, Elapsed: m.Elapsed, DaysLeft: m.DaysLeft, PerDayMinor: m.PerDayMinor,
		Lines: make([]BudgetLine, len(m.Lines)), Unbudgeted: make([]UnbudgetedCategory, len(m.Unbudgeted)),
	}
	for i, l := range m.Lines {
		out.Lines[i] = BudgetLine{CategoryID: l.CategoryID, BudgetGroupID: l.BudgetGroupID, PlannedMinor: l.PlannedMinor,
			SpentMinor: l.SpentMinor, RemainingMinor: l.RemainingMinor, ProjectedMinor: l.ProjectedMinor, Status: string(l.Status)}
	}
	for i, u := range m.Unbudgeted {
		out.Unbudgeted[i] = UnbudgetedCategory{CategoryID: u.CategoryID, SpentMinor: u.SpentMinor}
	}
	httpapi.CountResults(ctx, len(out.Lines))
	return &budgetMonthOutput{Body: out}, nil
}
