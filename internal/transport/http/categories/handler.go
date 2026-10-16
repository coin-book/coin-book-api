package categories

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/httpapi"
)

type Handler struct {
	repo *categorization.Repo
}

func NewHandler(repo *categorization.Repo) *Handler { return &Handler{repo: repo} }

type Category struct {
	ID            uuid.UUID  `json:"id"`
	Code          string     `json:"code"`
	ParentID      *uuid.UUID `json:"parent_id,omitempty"`
	NameCS        string     `json:"name_cs"`
	NameSK        string     `json:"name_sk"`
	Kind          string     `json:"kind" enum:"income,expense,financial,transfer"`
	ColorKey      *string    `json:"color_key,omitempty" doc:"Theme token key; absent on a subcategory, which uses its parent's."`
	Icon          *string    `json:"icon,omitempty"`
	BudgetGroupID *uuid.UUID `json:"budget_group_id,omitempty"`
	Sort          int        `json:"sort"`
}

type BudgetGroup struct {
	ID     uuid.UUID `json:"id"`
	Code   string    `json:"code"`
	NameCS string    `json:"name_cs"`
	NameSK string    `json:"name_sk"`
	Sort   int       `json:"sort"`
}

type taxonomyOutput struct {
	Body struct {
		Categories   []Category    `json:"categories"`
		BudgetGroups []BudgetGroup `json:"budget_groups"`
	}
}

type updateCategoryInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		NameCS        *string    `json:"name_cs,omitempty" minLength:"1" maxLength:"80"`
		NameSK        *string    `json:"name_sk,omitempty" minLength:"1" maxLength:"80"`
		ColorKey      *string    `json:"color_key,omitempty" maxLength:"40"`
		Icon          *string    `json:"icon,omitempty" maxLength:"40"`
		BudgetGroupID *uuid.UUID `json:"budget_group_id,omitempty"`
	}
}

type updateCategoryOutput struct {
	Body Category
}

type Rule struct {
	ID                       uuid.UUID `json:"id"`
	CategoryID               uuid.UUID `json:"category_id"`
	Priority                 int       `json:"priority"`
	MatchCounterparty        *string   `json:"match_counterparty,omitempty" doc:"Equal to the counterparty, ignoring case."`
	MatchDescription         *string   `json:"match_description,omitempty" doc:"Contained in the description, ignoring case."`
	MatchCounterpartyAccount *string   `json:"match_counterparty_account,omitempty"`
	MatchOperationType       *string   `json:"match_operation_type,omitempty"`
	MatchVariableSymbol      *string   `json:"match_variable_symbol,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
}

type rulesOutput struct {
	Body struct {
		Rules []Rule `json:"rules"`
	}
}

type createRuleInput struct {
	Body struct {
		CategoryID               uuid.UUID `json:"category_id"`
		Priority                 int       `json:"priority,omitempty" minimum:"0" maximum:"1000" default:"100"`
		MatchCounterparty        *string   `json:"match_counterparty,omitempty" minLength:"1" maxLength:"200"`
		MatchDescription         *string   `json:"match_description,omitempty" minLength:"1" maxLength:"200"`
		MatchCounterpartyAccount *string   `json:"match_counterparty_account,omitempty" minLength:"1" maxLength:"50"`
		MatchOperationType       *string   `json:"match_operation_type,omitempty" minLength:"1" maxLength:"60"`
		MatchVariableSymbol      *string   `json:"match_variable_symbol,omitempty" minLength:"1" maxLength:"10"`
	}
}

type createRuleOutput struct {
	Body Rule
}

type deleteRuleInput struct {
	ID uuid.UUID `path:"id"`
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeCategoriesRead, Operation: huma.Operation{
		OperationID: "list-categories", Method: http.MethodGet, Path: "/api/v1/categories", Tags: []string{"Categories"},
		Summary: "The two-level taxonomy and budget groups",
	}}, h.list)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "update-category", Method: http.MethodPatch, Path: "/api/v1/categories/{id}", Tags: []string{"Categories"},
		Summary: "Rename, recolour or regroup a category", Errors: []int{404, 422},
	}}, h.update)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeCategoriesRead, Operation: huma.Operation{
		OperationID: "list-rules", Method: http.MethodGet, Path: "/api/v1/rules", Tags: []string{"Categories"},
		Summary: "Categorisation rules, in the order they are tried",
	}}, h.rules)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "create-rule", Method: http.MethodPost, Path: "/api/v1/rules", Tags: []string{"Categories"},
		Summary: "Add a categorisation rule", DefaultStatus: http.StatusCreated, Errors: []int{422},
	}}, h.createRule)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "delete-rule", Method: http.MethodDelete, Path: "/api/v1/rules/{id}", Tags: []string{"Categories"},
		Summary: "Delete a rule; categories it already set stay", DefaultStatus: http.StatusNoContent, Errors: []int{404},
	}}, h.deleteRule)
}

func (h *Handler) list(ctx context.Context, _ *struct{}) (*taxonomyOutput, error) {
	cats, err := h.repo.Categories(ctx, nil)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	groups, err := h.repo.BudgetGroups(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &taxonomyOutput{}
	out.Body.Categories = make([]Category, len(cats))
	for i, c := range cats {
		out.Body.Categories[i] = toCategory(c)
	}
	out.Body.BudgetGroups = make([]BudgetGroup, len(groups))
	for i, g := range groups {
		out.Body.BudgetGroups[i] = BudgetGroup{ID: g.ID, Code: g.Code, NameCS: g.NameCS, NameSK: g.NameSK, Sort: g.Sort}
	}
	httpapi.CountResults(ctx, len(cats))
	return out, nil
}

func (h *Handler) update(ctx context.Context, in *updateCategoryInput) (*updateCategoryOutput, error) {
	c, err := h.repo.UpdateCategory(ctx, in.ID, categorization.CategoryUpdate{NameCS: in.Body.NameCS, NameSK: in.Body.NameSK,
		ColorKey: in.Body.ColorKey, Icon: in.Body.Icon, BudgetGroupID: in.Body.BudgetGroupID})
	if errors.Is(err, categorization.ErrCategoryNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &updateCategoryOutput{Body: toCategory(*c)}, nil
}

func (h *Handler) rules(ctx context.Context, _ *struct{}) (*rulesOutput, error) {
	rules, err := h.repo.Rules(ctx, nil)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	ordered := categorization.NewMatcher(rules).Rules()
	out := &rulesOutput{}
	out.Body.Rules = make([]Rule, len(ordered))
	for i, r := range ordered {
		out.Body.Rules[i] = toRule(r)
	}
	httpapi.CountResults(ctx, len(rules))
	return out, nil
}

func (h *Handler) createRule(ctx context.Context, in *createRuleInput) (*createRuleOutput, error) {
	b := in.Body
	rule := &categorization.Rule{CategoryID: b.CategoryID, Priority: b.Priority, MatchCounterparty: b.MatchCounterparty,
		MatchDescription: b.MatchDescription, MatchCounterpartyAccount: b.MatchCounterpartyAccount,
		MatchOperationType: b.MatchOperationType, MatchVariableSymbol: b.MatchVariableSymbol}
	if rule.Priority == 0 {
		rule.Priority = 100
	}
	if rule.MatchCounterparty == nil && rule.MatchDescription == nil && rule.MatchCounterpartyAccount == nil &&
		rule.MatchOperationType == nil && rule.MatchVariableSymbol == nil {
		return nil, apierr.Validation("body", "a rule needs at least one condition")
	}
	if err := h.repo.CreateRule(ctx, nil, rule); err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &createRuleOutput{Body: toRule(*rule)}, nil
}

func (h *Handler) deleteRule(ctx context.Context, in *deleteRuleInput) (*struct{}, error) {
	err := h.repo.DeleteRule(ctx, in.ID)
	if errors.Is(err, categorization.ErrRuleNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &struct{}{}, nil
}

func toCategory(c categorization.Category) Category {
	return Category{ID: c.ID, Code: c.Code, ParentID: c.ParentID, NameCS: c.NameCS, NameSK: c.NameSK, Kind: string(c.Kind),
		ColorKey: c.ColorKey, Icon: c.Icon, BudgetGroupID: c.BudgetGroupID, Sort: c.Sort}
}

func toRule(r categorization.Rule) Rule {
	return Rule{ID: r.ID, CategoryID: r.CategoryID, Priority: r.Priority, MatchCounterparty: r.MatchCounterparty,
		MatchDescription: r.MatchDescription, MatchCounterpartyAccount: r.MatchCounterpartyAccount,
		MatchOperationType: r.MatchOperationType, MatchVariableSymbol: r.MatchVariableSymbol, CreatedAt: r.CreatedAt}
}
