package transactions

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/transaction"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

type Handler struct {
	repo        *transaction.Repo
	categorizer *categorization.Service
}

func NewHandler(repo *transaction.Repo, categorizer *categorization.Service) *Handler {
	return &Handler{repo: repo, categorizer: categorizer}
}

type Assignment struct {
	CategoryID  uuid.UUID `json:"category_id"`
	AmountMinor int64     `json:"amount_minor"`
	Source      string    `json:"source" enum:"rule,manual,bank,model" doc:"model: the learned classifier chose it; confirm or change it."`
}

type Transfer struct {
	CounterpartTxID      *string   `json:"counterpart_tx_id,omitempty" doc:"Absent while the other account's statement is not imported."`
	CounterpartAccountID uuid.UUID `json:"counterpart_account_id"`
	MatchedBy            string    `json:"matched_by" enum:"auto,manual,number,counterparty" doc:"number and counterparty: recognised as a transfer to an own account."`
}

type Transaction struct {
	ID                  string       `json:"id"`
	AccountID           uuid.UUID    `json:"account_id"`
	BookedOn            string       `json:"booked_on" format:"date"`
	Amount              money.Amount `json:"amount"`
	FeeMinor            int64        `json:"fee_minor"`
	Description         string       `json:"description"`
	Counterparty        *string      `json:"counterparty,omitempty"`
	CounterpartyAccount *string      `json:"counterparty_account,omitempty"`
	OperationType       *string      `json:"operation_type,omitempty"`
	Message             *string      `json:"message,omitempty"`
	VariableSymbol      *string      `json:"variable_symbol,omitempty"`
	ConstantSymbol      *string      `json:"constant_symbol,omitempty"`
	SpecificSymbol      *string      `json:"specific_symbol,omitempty"`
	BankCategory        *string      `json:"bank_category,omitempty"`
	BalanceAfterMinor   *int64       `json:"balance_after_minor,omitempty"`
	ImportBatchID       uuid.UUID    `json:"import_batch_id"`
	SourceLine          int          `json:"source_line" doc:"Line in the imported file (F-IMP-6)."`
	Categories          []Assignment `json:"categories"`
	Transfer            *Transfer    `json:"transfer,omitempty" doc:"Set when this is one leg of a transfer between own accounts."`
	Suggestions         []Prediction `json:"suggestions,omitempty" doc:"Learned category suggestions; only on uncategorised transactions in lists."`
}

type Prediction struct {
	CategoryID uuid.UUID `json:"category_id"`
	Confidence float64   `json:"confidence" minimum:"0" maximum:"1"`
	Reasons    []string  `json:"reasons" doc:"Words of the transaction that point at this category."`
}

type listTransactionsInput struct {
	From          string    `query:"from" format:"date"`
	To            string    `query:"to" format:"date"`
	AccountID     uuid.UUID `query:"account_id"`
	CategoryID    uuid.UUID `query:"category_id" doc:"Matches the category and its subcategories."`
	Query         string    `query:"q" maxLength:"100" doc:"Substring of the description or counterparty, ignoring case and diacritics; or an amount such as 1249,50 (an explicit sign matches that direction only)."`
	Uncategorized bool      `query:"uncategorized" doc:"Only transactions with no category that are not transfers."`
	AutoAssigned  bool      `query:"auto" doc:"Only transactions the classifier categorised, awaiting confirmation."`
	Cursor        string    `query:"cursor"`
	Limit         int       `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type listTransactionsOutput struct {
	Body struct {
		Transactions []Transaction `json:"transactions"`
		NextCursor   string        `json:"next_cursor,omitempty"`
	}
}

type getInput struct {
	ID string `path:"id" maxLength:"64"`
}

type transactionOutput struct {
	Body Transaction
}

type CategoryLine struct {
	CategoryID  uuid.UUID `json:"category_id"`
	AmountMinor int64     `json:"amount_minor"`
}

type categorizeInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Lines    []CategoryLine `json:"lines" minItems:"1" maxItems:"20" doc:"One line, or several for a split; they must add up to the amount."`
		SaveRule *string        `json:"save_rule,omitempty" enum:"counterparty,description,counterparty_account" doc:"Turn a single-line choice into a rule matching on this field."`
	}
}

type categorizeOutput struct {
	Body struct {
		Transaction    Transaction    `json:"transaction"`
		RuleID         *uuid.UUID     `json:"rule_id,omitempty"`
		AppliedToOther int            `json:"applied_to_other" doc:"Other transactions the new rule categorised."`
		SuggestedRule  *SuggestedRule `json:"suggested_rule,omitempty" doc:"Offered when the same choice for this counterparty keeps repeating."`
	}
}

type SuggestedRule struct {
	Counterparty string    `json:"counterparty"`
	CategoryID   uuid.UUID `json:"category_id"`
	Times        int       `json:"times"`
}

type confirmInput struct {
	ID string `path:"id" maxLength:"64"`
}

type confirmOutput struct {
	Body struct {
		Confirmed int `json:"confirmed"`
	}
}

type Suggestion struct {
	CategoryID uuid.UUID `json:"category_id"`
	Uses       int       `json:"uses"`
}

type triageInput struct {
	Skip []string `query:"skip" maxItems:"100" doc:"Transactions the user skipped in this session."`
	ID   string   `query:"id" maxLength:"64" doc:"Show this transaction while it is still uncategorised, even if skipped; otherwise the next one."`
}

type triageOutput struct {
	Body struct {
		Transaction *Transaction `json:"transaction,omitempty" doc:"Absent when nothing is left to categorise."`
		Remaining   int          `json:"remaining"`
		Suggested   []Prediction `json:"suggested" doc:"Learned suggestions, most likely first."`
		QuickPicks  []Suggestion `json:"quick_picks" doc:"Most used categories lately, for the 1–9 keys."`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeTransactionsRead, Operation: huma.Operation{
		OperationID: "list-transactions", Method: http.MethodGet, Path: "/api/v1/transactions", Tags: []string{"Transactions"},
		Summary: "Transactions, newest first", Errors: []int{422},
	}}, h.list)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeTransactionsRead, Operation: huma.Operation{
		OperationID: "get-transaction", Method: http.MethodGet, Path: "/api/v1/transactions/{id}", Tags: []string{"Transactions"},
		Summary: "One transaction", Errors: []int{404},
	}}, h.get)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "categorize-transaction", Method: http.MethodPut, Path: "/api/v1/transactions/{id}/categories",
		Tags: []string{"Transactions"}, Summary: "Set the category (or split) by hand, optionally saving a rule",
		Errors: []int{404, 422},
	}}, h.categorize)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "confirm-transaction-category", Method: http.MethodPost, Path: "/api/v1/transactions/{id}/confirm",
		Tags: []string{"Transactions"}, Summary: "Accept the category the classifier chose",
	}}, h.confirm)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "confirm-all-automatic-categories", Method: http.MethodPost, Path: "/api/v1/transactions/confirm-automatic",
		Tags: []string{"Transactions"}, Summary: "Accept every category the classifier chose",
	}}, h.confirmAll)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "get-triage", Method: http.MethodGet, Path: "/api/v1/triage", Tags: []string{"Transactions"},
		Summary: "The next uncategorised transaction with suggestions",
	}}, h.triage)
}

func (h *Handler) list(ctx context.Context, in *listTransactionsInput) (*listTransactionsOutput, error) {
	from, err := dto.ParseDate(in.From, "query.from")
	if err != nil {
		return nil, err
	}
	to, err := dto.ParseDate(in.To, "query.to")
	if err != nil {
		return nil, err
	}
	f := transaction.Filter{From: from, To: to, Query: in.Query, Uncategorized: in.Uncategorized, AutoAssigned: in.AutoAssigned,
		Cursor: in.Cursor, Limit: in.Limit}
	if f.Limit == 0 || f.Limit > maxPageSize {
		f.Limit = defaultPageSize
	}
	if in.AccountID != uuid.Nil {
		f.AccountID = &in.AccountID
	}
	if in.CategoryID != uuid.Nil {
		f.CategoryID = &in.CategoryID
	}
	page, err := h.repo.List(ctx, f)
	if errors.Is(err, transaction.ErrInvalidCursor) {
		return nil, apierr.Unprocessable(apierr.CodeInvalidCursor)
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	var uncategorized []string
	for _, item := range page.Items {
		if len(item.Assignments) == 0 && item.Transfer == nil {
			uncategorized = append(uncategorized, item.ID)
		}
	}
	suggestions, err := h.categorizer.Suggest(ctx, uncategorized)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listTransactionsOutput{}
	out.Body.Transactions = make([]Transaction, 0, len(page.Items))
	for _, item := range page.Items {
		dto := ToDTO(item)
		dto.Suggestions = predictions(suggestions[item.ID])
		out.Body.Transactions = append(out.Body.Transactions, dto)
	}
	out.Body.NextCursor = page.NextCursor
	httpapi.CountResults(ctx, len(page.Items))
	return out, nil
}

func (h *Handler) get(ctx context.Context, in *getInput) (*transactionOutput, error) {
	t, err := h.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	httpapi.CountResults(ctx, 1)
	return &transactionOutput{Body: *t}, nil
}

func (h *Handler) load(ctx context.Context, id string) (*Transaction, error) {
	t, err := h.repo.Get(ctx, id)
	if errors.Is(err, transaction.ErrNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := ToDTO(*t)
	return &out, nil
}

func (h *Handler) categorize(ctx context.Context, in *categorizeInput) (*categorizeOutput, error) {
	lines := make([]categorization.Line, len(in.Body.Lines))
	for i, l := range in.Body.Lines {
		lines[i] = categorization.Line{CategoryID: l.CategoryID, AmountMinor: l.AmountMinor}
	}
	var match *categorization.RuleMatch
	if in.Body.SaveRule != nil {
		m := categorization.RuleMatch(*in.Body.SaveRule)
		match = &m
	}
	result, err := h.categorizer.Assign(ctx, in.ID, lines, match)
	switch {
	case errors.Is(err, categorization.ErrTransactionNotFound):
		return nil, apierr.NotFound()
	case errors.Is(err, categorization.ErrSplitMismatch), errors.Is(err, categorization.ErrNoLines):
		return nil, apierr.Unprocessable(apierr.CodeSplitMismatch)
	case errors.Is(err, categorization.ErrNothingToMatch):
		return nil, apierr.Unprocessable(apierr.CodeNothingToMatch)
	case errors.Is(err, categorization.ErrCategoryNotFound):
		return nil, apierr.Validation("body.lines", "unknown category")
	case err != nil:
		return nil, apierr.Internal(ctx, err)
	}
	t, err := h.load(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	out := &categorizeOutput{}
	out.Body.Transaction = *t
	out.Body.AppliedToOther = result.AppliedToOther
	if result.Rule != nil {
		out.Body.RuleID = &result.Rule.ID
	}
	if r := result.SuggestedRule; r != nil {
		out.Body.SuggestedRule = &SuggestedRule{Counterparty: r.Counterparty, CategoryID: r.CategoryID, Times: r.Times}
	}
	return out, nil
}

func (h *Handler) triage(ctx context.Context, in *triageInput) (*triageOutput, error) {
	remaining, err := h.repo.CountUncategorized(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	next, err := h.categorizer.NextForTriage(ctx, remaining, withoutFocus(in.Skip, in.ID), in.ID)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &triageOutput{}
	out.Body.Remaining = next.Remaining
	out.Body.Suggested = predictions(next.Suggested)
	out.Body.QuickPicks = suggestions(next.QuickPicks)
	if next.TransactionID != "" {
		if out.Body.Transaction, err = h.load(ctx, next.TransactionID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// withoutFocus lets a transaction the user picks from the queue show even when it was skipped.
func withoutFocus(skip []string, focus string) []string {
	return slices.DeleteFunc(slices.Clone(skip), func(id string) bool { return id == focus })
}

func predictions(in []categorization.Prediction) []Prediction {
	out := make([]Prediction, len(in))
	for i, p := range in {
		reasons := p.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		out[i] = Prediction{CategoryID: p.CategoryID, Confidence: p.Confidence, Reasons: reasons}
	}
	return out
}

func (h *Handler) confirm(ctx context.Context, in *confirmInput) (*confirmOutput, error) {
	return h.confirmIDs(ctx, []string{in.ID})
}

func (h *Handler) confirmAll(ctx context.Context, _ *struct{}) (*confirmOutput, error) {
	return h.confirmIDs(ctx, nil)
}

func (h *Handler) confirmIDs(ctx context.Context, ids []string) (*confirmOutput, error) {
	n, err := h.categorizer.Confirm(ctx, ids)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &confirmOutput{}
	out.Body.Confirmed = n
	return out, nil
}

func suggestions(in []categorization.Suggestion) []Suggestion {
	out := make([]Suggestion, len(in))
	for i, s := range in {
		out[i] = Suggestion{CategoryID: s.CategoryID, Uses: s.Uses}
	}
	return out
}

func ToDTO(t transaction.Listed) Transaction {
	out := Transaction{
		ID: t.ID, AccountID: t.AccountID, BookedOn: dto.Date(t.BookedOn), Amount: t.Amount, FeeMinor: t.FeeMinor,
		Description: t.Description, Counterparty: t.Counterparty, CounterpartyAccount: t.CounterpartyAccount,
		OperationType: t.OperationType, Message: t.Message, VariableSymbol: t.VariableSymbol,
		ConstantSymbol: t.ConstantSymbol, SpecificSymbol: t.SpecificSymbol, BankCategory: t.BankCategory,
		BalanceAfterMinor: t.BalanceAfterMinor, ImportBatchID: t.ImportBatchID, SourceLine: t.SourceLine,
		Categories: make([]Assignment, len(t.Assignments)),
	}
	for i, a := range t.Assignments {
		out.Categories[i] = Assignment{CategoryID: a.CategoryID, AmountMinor: a.AmountMinor, Source: a.Source}
	}
	if t.Transfer != nil {
		out.Transfer = &Transfer{CounterpartTxID: t.Transfer.CounterpartTxID,
			CounterpartAccountID: t.Transfer.CounterpartAccountID, MatchedBy: t.Transfer.MatchedBy}
	}
	return out
}
