package transfers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/transfer"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/httpapi"
)

const listLimit = 500

type Handler struct {
	service *transfer.Service
}

func NewHandler(service *transfer.Service) *Handler { return &Handler{service: service} }

type TransferPair struct {
	OutTxID   string    `json:"out_tx_id"`
	InTxID    string    `json:"in_tx_id"`
	MatchedBy string    `json:"matched_by" enum:"auto,manual"`
	CreatedAt time.Time `json:"created_at"`
}

type listTransfersOutput struct {
	Body struct {
		Transfers []TransferPair `json:"transfers"`
	}
}

type AmbiguousTransfer struct {
	TransactionID string   `json:"transaction_id" doc:"The outgoing leg."`
	CandidateIDs  []string `json:"candidate_ids" doc:"Incoming movements that could be its other leg."`
	NumberMatches []string `json:"number_matches" doc:"Candidates whose account number one leg names as its counterparty account: the likelier ones."`
}

type listAmbiguousOutput struct {
	Body struct {
		AmbiguousTransfer []AmbiguousTransfer `json:"ambiguous"`
	}
}

type pairTransferInput struct {
	Body struct {
		OutTxID string `json:"out_tx_id" maxLength:"64"`
		InTxID  string `json:"in_tx_id" maxLength:"64"`
	}
}

type listCounterpartsInput struct {
	TxID string `path:"tx_id" maxLength:"64"`
}

type TransferCounterpart struct {
	TransactionID string `json:"transaction_id"`
	NumberMatches bool   `json:"number_matches" doc:"One leg names the other's account number as its counterparty: a likelier match."`
}

type listCounterpartsOutput struct {
	Body struct {
		Counterparts []TransferCounterpart `json:"counterparts"`
	}
}

type pairCounterpartInput struct {
	TxID string `path:"tx_id" maxLength:"64"`
	Body struct {
		CounterpartTxID string `json:"counterpart_tx_id" maxLength:"64"`
	}
}

type pairCounterpartOutput struct {
	Body struct {
		Released []string `json:"released" doc:"Former partners of either leg, no longer part of any transfer."`
	}
}

type unpairInput struct {
	TxID string `path:"tx_id" maxLength:"64" doc:"Either leg of the transfer."`
}

type markOwnInput struct {
	TxID string `path:"tx_id" maxLength:"64"`
	Body struct {
		AccountID            uuid.UUID `json:"account_id" doc:"The own account the money went to or came from."`
		RememberCounterparty bool      `json:"remember_counterparty,omitempty" doc:"Treat every payment with this counterparty the same way."`
	}
}

type markOwnOutput struct {
	Body struct {
		AppliedToOther int `json:"applied_to_other"`
	}
}

type OwnAccountCandidate struct {
	AccountNumber string    `json:"account_number"`
	BankID        string    `json:"bank_id,omitempty"`
	Counterparty  string    `json:"counterparty,omitempty" doc:"The name the payments most often carry."`
	Transactions  int       `json:"transactions"`
	OutMinor      int64     `json:"out_minor" doc:"Sent to the account."`
	InMinor       int64     `json:"in_minor" doc:"Received from the account."`
	Currency      string    `json:"currency"`
	LastBookedOn  time.Time `json:"last_booked_on" format:"date"`
}

type ownCandidatesOutput struct {
	Body struct {
		Candidates []OwnAccountCandidate `json:"candidates"`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "mark-own-transfer", Method: http.MethodPut, Path: "/api/v1/transfers/{tx_id}/own",
		Tags: []string{"Transfers"}, Summary: "Mark a payment as a transfer to or from an own account",
		Description: "For an account whose statement is not imported; the payment then counts as neither income nor expense.",
		Errors:      []int{422},
	}}, h.markOwn)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "list-own-account-candidates", Method: http.MethodGet, Path: "/api/v1/transfers/own-account-candidates",
		Tags: []string{"Transfers"}, Summary: "Counterparty accounts the bank calls the client's own that coin-book does not know",
	}}, h.ownCandidates)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeTransactionsRead, Operation: huma.Operation{
		OperationID: "list-transfers", Method: http.MethodGet, Path: "/api/v1/transfers", Tags: []string{"Transfers"},
		Summary: "Paired transfers between own accounts, newest first",
	}}, h.list)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "list-ambiguous-transfers", Method: http.MethodGet, Path: "/api/v1/transfers/ambiguous",
		Tags: []string{"Transfers"}, Summary: "Outgoing movements with more than one possible counterpart (F-PRE-2)",
	}}, h.ambiguous)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "pair-transfer", Method: http.MethodPost, Path: "/api/v1/transfers", Tags: []string{"Transfers"},
		Summary: "TransferPair two transactions as a transfer by hand", DefaultStatus: http.StatusCreated, Errors: []int{422},
	}}, h.pair)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "list-transfer-counterparts", Method: http.MethodGet, Path: "/api/v1/transfers/{tx_id}/counterparts",
		Tags: []string{"Transfers"}, Summary: "Movements that could be the other leg of the transaction's transfer, likeliest first",
		Description: "Includes movements already in another transfer; the transaction's current partner is left out.",
	}}, h.counterparts)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "pair-transfer-counterpart", Method: http.MethodPut, Path: "/api/v1/transfers/{tx_id}/counterpart",
		Tags: []string{"Transfers"}, Summary: "Pair the transaction with another counterpart by hand",
		Description: "Any transfer either transaction was part of is undone first.", Errors: []int{422},
	}}, h.pairCounterpart)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "unpair-transfer", Method: http.MethodDelete, Path: "/api/v1/transfers/{tx_id}", Tags: []string{"Transfers"},
		Summary: "Undo a transfer; a one-leg transfer is remembered as not a transfer", DefaultStatus: http.StatusNoContent, Errors: []int{404},
	}}, h.unpair)
}

func (h *Handler) markOwn(ctx context.Context, in *markOwnInput) (*markOwnOutput, error) {
	others, err := h.service.MarkOwn(ctx, in.TxID, in.Body.AccountID, in.Body.RememberCounterparty)
	if errors.Is(err, transfer.ErrNotOwnAccount) {
		return nil, apierr.Unprocessable(apierr.CodeNotOwnAccount)
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &markOwnOutput{}
	out.Body.AppliedToOther = others
	return out, nil
}

func (h *Handler) ownCandidates(ctx context.Context, _ *struct{}) (*ownCandidatesOutput, error) {
	candidates, err := h.service.OwnAccountCandidates(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &ownCandidatesOutput{}
	out.Body.Candidates = make([]OwnAccountCandidate, len(candidates))
	for i, c := range candidates {
		out.Body.Candidates[i] = OwnAccountCandidate{AccountNumber: c.AccountNumber, BankID: c.BankID, Counterparty: c.Counterparty,
			Transactions: c.Transactions, OutMinor: c.OutMinor, InMinor: c.InMinor, Currency: c.Currency, LastBookedOn: c.LastBookedOn}
	}
	return out, nil
}

func (h *Handler) list(ctx context.Context, _ *struct{}) (*listTransfersOutput, error) {
	pairs, err := h.service.List(ctx, listLimit)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listTransfersOutput{}
	out.Body.Transfers = make([]TransferPair, len(pairs))
	for i, p := range pairs {
		out.Body.Transfers[i] = TransferPair{OutTxID: p.OutTxID, InTxID: p.InTxID, MatchedBy: string(p.MatchedBy), CreatedAt: p.CreatedAt}
	}
	httpapi.CountResults(ctx, len(pairs))
	return out, nil
}

func (h *Handler) ambiguous(ctx context.Context, _ *struct{}) (*listAmbiguousOutput, error) {
	candidates, err := h.service.Ambiguous(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listAmbiguousOutput{}
	out.Body.AmbiguousTransfer = make([]AmbiguousTransfer, len(candidates))
	for i, c := range candidates {
		out.Body.AmbiguousTransfer[i] = AmbiguousTransfer{TransactionID: c.TransactionID, CandidateIDs: c.CandidateIDs,
			NumberMatches: append([]string{}, c.NumberMatches...)}
	}
	return out, nil
}

func (h *Handler) pair(ctx context.Context, in *pairTransferInput) (*struct{}, error) {
	err := h.service.Pair(ctx, in.Body.OutTxID, in.Body.InTxID)
	if errors.Is(err, transfer.ErrNotPairable) {
		return nil, apierr.Unprocessable(apierr.CodeNotPairable)
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) counterparts(ctx context.Context, in *listCounterpartsInput) (*listCounterpartsOutput, error) {
	counterparts, err := h.service.Counterparts(ctx, in.TxID)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listCounterpartsOutput{}
	out.Body.Counterparts = make([]TransferCounterpart, len(counterparts))
	for i, c := range counterparts {
		out.Body.Counterparts[i] = TransferCounterpart{TransactionID: c.TransactionID, NumberMatches: c.NumberMatches}
	}
	return out, nil
}

func (h *Handler) pairCounterpart(ctx context.Context, in *pairCounterpartInput) (*pairCounterpartOutput, error) {
	released, err := h.service.PairWithCounterpart(ctx, in.TxID, in.Body.CounterpartTxID)
	if errors.Is(err, transfer.ErrNotPairable) {
		return nil, apierr.Unprocessable(apierr.CodeNotPairable)
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &pairCounterpartOutput{}
	out.Body.Released = append([]string{}, released...)
	return out, nil
}

func (h *Handler) unpair(ctx context.Context, in *unpairInput) (*struct{}, error) {
	err := h.service.Unpair(ctx, in.TxID)
	if errors.Is(err, transfer.ErrNotPaired) {
		return nil, apierr.New(http.StatusNotFound, apierr.CodeNotPaired)
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &struct{}{}, nil
}
