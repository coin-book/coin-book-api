package accounts

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/bank"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

// OwnTransferDetector marks earlier payments to an account that was just added.
type OwnTransferDetector interface {
	DetectOwn(ctx context.Context, q db.Querier, transactionIDs []string) (int, error)
}

type Handler struct {
	repo      *account.Repo
	banks     *bank.Repo
	transfers OwnTransferDetector
	pool      db.Querier
}

func NewHandler(repo *account.Repo, banks *bank.Repo, transfers OwnTransferDetector, pool db.Querier) *Handler {
	return &Handler{repo: repo, banks: banks, transfers: transfers, pool: pool}
}

type Account struct {
	ID               uuid.UUID    `json:"id"`
	Name             string       `json:"name"`
	Institution      string       `json:"institution" enum:"mbank_cz,mbank_sk,revolut,ibkr,other"`
	Kind             string       `json:"kind" enum:"current,savings,pocket,fund,broker,cash,card"`
	Currency         string       `json:"currency"`
	AccountNumber    *string      `json:"account_number,omitempty"`
	BankID           *string      `json:"bank_id,omitempty" doc:"Bank register id, see GET /banks."`
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
		Kind                *string `json:"kind,omitempty" enum:"current,savings,pocket,fund,broker,cash,card"`
		OpeningBalanceMinor *int64  `json:"opening_balance_minor,omitempty"`
		ColorKey            *string `json:"color_key,omitempty" maxLength:"40"`
		Archived            *bool   `json:"archived,omitempty"`
		BankID              *string `json:"bank_id,omitempty" maxLength:"40" doc:"Empty string clears the bank."`
	}
}

type updateAccountOutput struct {
	Body Account
}

type createInput struct {
	Body struct {
		Name                string  `json:"name" minLength:"1" maxLength:"100"`
		Kind                string  `json:"kind" enum:"current,savings,pocket,fund,broker,cash,card"`
		Currency            string  `json:"currency" pattern:"^[A-Z]{3}$"`
		AccountNumber       *string `json:"account_number,omitempty" maxLength:"50" doc:"Bank number or IBAN; imports of that account then land here."`
		BankID              *string `json:"bank_id,omitempty" maxLength:"40" doc:"Bank register id; detected from the account number when omitted."`
		OpeningBalanceMinor int64   `json:"opening_balance_minor,omitempty"`
	}
}

type mergeInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		TargetID uuid.UUID `json:"target_id" doc:"The account that keeps everything; this one is deleted."`
	}
}

type mergeOutput struct {
	Body struct {
		Moved int `json:"moved" doc:"Transactions moved into the target account."`
	}
}

type Alias struct {
	ID          uuid.UUID `json:"id"`
	Institution string    `json:"institution"`
	Identity    string    `json:"identity" doc:"The bank account number or importer reference that is mapped."`
	AccountID   uuid.UUID `json:"account_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type listAliasesOutput struct {
	Body struct {
		Aliases []Alias `json:"aliases"`
	}
}

type deleteAliasInput struct {
	ID uuid.UUID `path:"id"`
}

type Bank struct {
	ID        string  `json:"id" example:"cz-0800"`
	Country   *string `json:"country,omitempty"`
	Code      *string `json:"code,omitempty" doc:"Domestic bank code (CZ: after the slash, SK: in the IBAN)."`
	Name      string  `json:"name"`
	ShortName string  `json:"short_name"`
	BIC       *string `json:"bic,omitempty"`
}

type listBanksOutput struct {
	Body struct {
		Banks []Bank `json:"banks"`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "list-banks", Method: http.MethodGet, Path: "/api/v1/banks", Tags: []string{"Accounts"},
		Summary: "The bank register: Czech and Slovak bank codes plus fintechs",
	}}, h.listBanks)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "create-account", Method: http.MethodPost, Path: "/api/v1/accounts", Tags: []string{"Accounts"},
		Summary: "Create an account by hand, e.g. cash or a bank without a parser", DefaultStatus: http.StatusCreated,
		Errors: []int{409, 422},
	}}, h.create)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "merge-account", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/merge", Tags: []string{"Accounts"},
		Summary: "Move every transaction into another account of the same currency and delete this one",
		Errors:  []int{404, 422},
	}}, h.merge)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "list-account-aliases", Method: http.MethodGet, Path: "/api/v1/account-aliases", Tags: []string{"Accounts"},
		Summary: "Remembered mappings of bank accounts onto own accounts",
	}}, h.aliases)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "delete-account-alias", Method: http.MethodDelete, Path: "/api/v1/account-aliases/{id}", Tags: []string{"Accounts"},
		Summary: "Forget a remembered mapping", DefaultStatus: http.StatusNoContent, Errors: []int{404},
	}}, h.deleteAlias)
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
		ColorKey: in.Body.ColorKey, Archived: in.Body.Archived, BankID: in.Body.BankID}
	if u.BankID != nil && *u.BankID != "" {
		if err := h.requireKnownBank(ctx, *u.BankID); err != nil {
			return nil, err
		}
	}
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
		AccountNumber: a.AccountNumber, BankID: a.BankID, OpeningBalance: money.New(a.OpeningBalanceMinor, a.Currency),
		Balance: money.New(a.OpeningBalanceMinor, a.Currency), ColorKey: a.ColorKey, Archived: a.Archived,
	}
}

func (h *Handler) create(ctx context.Context, in *createInput) (*updateAccountOutput, error) {
	b := in.Body
	a := &account.Account{Name: strings.TrimSpace(b.Name), Institution: account.Other, Kind: account.Kind(b.Kind),
		Currency: money.Currency(b.Currency), OpeningBalanceMinor: b.OpeningBalanceMinor}
	if b.AccountNumber != nil && strings.TrimSpace(*b.AccountNumber) != "" {
		number := strings.TrimSpace(*b.AccountNumber)
		a.AccountNumber = &number
	} else {
		ref := account.ManualRef()
		a.ExternalRef = &ref
	}
	if b.BankID != nil && *b.BankID != "" {
		if err := h.requireKnownBank(ctx, *b.BankID); err != nil {
			return nil, err
		}
		a.BankID = b.BankID
	} else {
		detected, err := bank.Detect(ctx, h.pool, a.AccountNumber)
		if err != nil {
			return nil, apierr.Internal(ctx, err)
		}
		a.BankID = detected
	}
	err := h.repo.Create(ctx, h.pool, a)
	if db.IsUniqueViolation(err) {
		return nil, apierr.Conflict(apierr.CodeAccountExists)
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	if _, err := h.transfers.DetectOwn(ctx, h.pool, nil); err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &updateAccountOutput{Body: toDTO(a)}, nil
}

func (h *Handler) merge(ctx context.Context, in *mergeInput) (*mergeOutput, error) {
	moved, err := h.repo.Merge(ctx, in.ID, in.Body.TargetID)
	switch {
	case errors.Is(err, account.ErrNotFound):
		return nil, apierr.NotFound()
	case errors.Is(err, account.ErrCurrencyMismatch):
		return nil, apierr.Unprocessable(apierr.CodeCurrencyMismatch)
	case errors.Is(err, account.ErrMergeIntoItself):
		return nil, apierr.Unprocessable(apierr.CodeMergeIntoItself)
	case err != nil:
		return nil, apierr.Internal(ctx, err)
	}
	out := &mergeOutput{}
	out.Body.Moved = moved
	return out, nil
}

func (h *Handler) aliases(ctx context.Context, _ *struct{}) (*listAliasesOutput, error) {
	aliases, err := h.repo.Aliases(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listAliasesOutput{}
	out.Body.Aliases = make([]Alias, len(aliases))
	for i, a := range aliases {
		out.Body.Aliases[i] = Alias{ID: a.ID, Institution: string(a.Institution), Identity: a.Identity, AccountID: a.AccountID, CreatedAt: a.CreatedAt}
	}
	return out, nil
}

func (h *Handler) deleteAlias(ctx context.Context, in *deleteAliasInput) (*struct{}, error) {
	err := h.repo.DeleteAlias(ctx, in.ID)
	if errors.Is(err, account.ErrAliasNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) listBanks(ctx context.Context, _ *struct{}) (*listBanksOutput, error) {
	banks, err := h.banks.List(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listBanksOutput{}
	out.Body.Banks = make([]Bank, len(banks))
	for i, b := range banks {
		out.Body.Banks[i] = Bank{ID: b.ID, Country: b.Country, Code: b.Code, Name: b.Name, ShortName: b.ShortName, BIC: b.BIC}
	}
	httpapi.CountResults(ctx, len(banks))
	return out, nil
}

func (h *Handler) requireKnownBank(ctx context.Context, id string) error {
	known, err := bank.Known(ctx, h.pool, id)
	if err != nil {
		return apierr.Internal(ctx, err)
	}
	if known == nil {
		return apierr.Unprocessable(apierr.CodeUnknownBank)
	}
	return nil
}
