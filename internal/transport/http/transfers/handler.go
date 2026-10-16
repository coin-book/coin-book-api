package transfers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

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

type unpairInput struct {
	TxID string `path:"tx_id" maxLength:"64" doc:"Either leg of the transfer."`
}

func (h *Handler) Register(api huma.API) {
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
		OperationID: "unpair-transfer", Method: http.MethodDelete, Path: "/api/v1/transfers/{tx_id}", Tags: []string{"Transfers"},
		Summary: "Undo a transfer pairing", DefaultStatus: http.StatusNoContent, Errors: []int{404},
	}}, h.unpair)
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
		out.Body.AmbiguousTransfer[i] = AmbiguousTransfer{TransactionID: c.TransactionID, CandidateIDs: c.CandidateIDs}
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
