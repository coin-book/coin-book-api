package accounts

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

type Handler struct {
	repo *account.Repo
}

func NewHandler(repo *account.Repo) *Handler { return &Handler{repo: repo} }

type Account struct {
	ID               uuid.UUID    `json:"id"`
	Name             string       `json:"name"`
	Institution      string       `json:"institution" enum:"mbank_cz,mbank_sk,revolut,ibkr,other"`
	Kind             string       `json:"kind" enum:"current,savings,pocket,fund,broker"`
	Currency         string       `json:"currency"`
	AccountNumber    *string      `json:"account_number,omitempty"`
	OpeningBalance   money.Amount `json:"opening_balance"`
	Balance          money.Amount `json:"balance" doc:"Opening balance plus every imported movement."`
	TransactionCount int          `json:"transaction_count"`
	LastBookedOn     *string      `json:"last_booked_on,omitempty" format:"date"`
	ColorKey         *string      `json:"color_key,omitempty"`
	Archived         bool         `json:"archived"`
}

type listAccountsOutput struct {
	Body struct {
		Accounts []Account `json:"accounts"`
	}
}

type updateAccountInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		Name                *string `json:"name,omitempty" minLength:"1" maxLength:"100"`
		Kind                *string `json:"kind,omitempty" enum:"current,savings,pocket,fund,broker"`
		OpeningBalanceMinor *int64  `json:"opening_balance_minor,omitempty"`
		ColorKey            *string `json:"color_key,omitempty" maxLength:"40"`
		Archived            *bool   `json:"archived,omitempty"`
	}
}

type updateAccountOutput struct {
	Body Account
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "list-accounts", Method: http.MethodGet, Path: "/api/v1/accounts", Tags: []string{"Accounts"},
		Summary: "Own accounts with current balances",
	}}, h.list)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "update-account", Method: http.MethodPatch, Path: "/api/v1/accounts/{id}", Tags: []string{"Accounts"},
		Summary: "Rename an account, set its opening balance or archive it", Errors: []int{404, 422},
	}}, h.update)
}

func (h *Handler) list(ctx context.Context, _ *struct{}) (*listAccountsOutput, error) {
	accounts, err := h.repo.ListWithBalances(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listAccountsOutput{}
	out.Body.Accounts = make([]Account, 0, len(accounts))
	for _, a := range accounts {
		dtoAccount := toDTO(&a.Account)
		dtoAccount.Balance = a.Balance
		dtoAccount.TransactionCount = a.TransactionCount
		dtoAccount.LastBookedOn = dto.DatePtr(a.LastBookedOn)
		out.Body.Accounts = append(out.Body.Accounts, dtoAccount)
	}
	httpapi.CountResults(ctx, len(accounts))
	return out, nil
}

func (h *Handler) update(ctx context.Context, in *updateAccountInput) (*updateAccountOutput, error) {
	u := account.Update{Name: in.Body.Name, OpeningBalanceMinor: in.Body.OpeningBalanceMinor,
		ColorKey: in.Body.ColorKey, Archived: in.Body.Archived}
	if in.Body.Kind != nil {
		kind := account.Kind(*in.Body.Kind)
		u.Kind = &kind
	}
	a, err := h.repo.Update(ctx, in.ID, u)
	if errors.Is(err, account.ErrNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &updateAccountOutput{Body: toDTO(a)}, nil
}

func toDTO(a *account.Account) Account {
	return Account{
		ID: a.ID, Name: a.Name, Institution: string(a.Institution), Kind: string(a.Kind), Currency: string(a.Currency),
		AccountNumber: a.AccountNumber, OpeningBalance: money.New(a.OpeningBalanceMinor, a.Currency),
		Balance: money.New(a.OpeningBalanceMinor, a.Currency), ColorKey: a.ColorKey, Archived: a.Archived,
	}
}
