package agenttokens

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/httpapi"
)

const (
	auditPageSize = 100
	day           = 24 * time.Hour
)

type Handler struct {
	repo *agentaccess.Repo
}

func NewHandler(repo *agentaccess.Repo) *Handler { return &Handler{repo: repo} }

type AgentToken struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Last4      string     `json:"last4"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type listAgentTokensOutput struct {
	Body struct {
		Tokens []AgentToken `json:"tokens"`
	}
}

type createAgentTokenInput struct {
	Body struct {
		Name          string   `json:"name" minLength:"1" maxLength:"80"`
		Scopes        []string `json:"scopes" minItems:"1" enum:"accounts:read,transactions:read,categories:read,budgets:read,reports:read,investments:read"`
		ExpiresInDays *int     `json:"expires_in_days,omitempty" minimum:"1" maximum:"365" doc:"Omit for a token that does not expire."`
	}
}

type createAgentTokenOutput struct {
	Body struct {
		AgentToken AgentToken `json:"token"`
		Secret     string     `json:"secret" doc:"The token itself. Shown only in this response."`
	}
}

type revokeInput struct {
	ID uuid.UUID `path:"id"`
}

type auditInput struct {
	Before int64 `query:"before" doc:"Return entries older than this id."`
}

type AuditEntry struct {
	ID          int64     `json:"id"`
	TokenID     uuid.UUID `json:"token_id"`
	TokenName   string    `json:"token_name"`
	Operation   string    `json:"operation"`
	Status      int       `json:"status"`
	ResultCount *int      `json:"result_count,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
}

type listAuditOutput struct {
	Body struct {
		Entries []AuditEntry `json:"entries"`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "list-agent-tokens", Method: http.MethodGet, Path: "/api/v1/agent-tokens", Tags: []string{"Agent access"},
		Summary: "Agent tokens, including revoked ones",
	}}, h.list)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "create-agent-token", Method: http.MethodPost, Path: "/api/v1/agent-tokens", Tags: []string{"Agent access"},
		Summary: "Issue a read-only token for an AI agent", DefaultStatus: http.StatusCreated, Errors: []int{422},
	}}, h.create)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "revoke-agent-token", Method: http.MethodDelete, Path: "/api/v1/agent-tokens/{id}", Tags: []string{"Agent access"},
		Summary: "Revoke a token", DefaultStatus: http.StatusNoContent, Errors: []int{404},
	}}, h.revoke)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "list-audit-log", Method: http.MethodGet, Path: "/api/v1/audit-log", Tags: []string{"Agent access"},
		Summary: "Every request made with an agent token, newest first",
	}}, h.audit)
}

func (h *Handler) list(ctx context.Context, _ *struct{}) (*listAgentTokensOutput, error) {
	tokens, err := h.repo.List(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listAgentTokensOutput{}
	out.Body.Tokens = make([]AgentToken, len(tokens))
	for i, t := range tokens {
		out.Body.Tokens[i] = toToken(t)
	}
	return out, nil
}

func (h *Handler) create(ctx context.Context, in *createAgentTokenInput) (*createAgentTokenOutput, error) {
	scopes := make([]agentaccess.Scope, len(in.Body.Scopes))
	for i, s := range in.Body.Scopes {
		scopes[i] = agentaccess.Scope(s)
	}
	var ttl *time.Duration
	if in.Body.ExpiresInDays != nil {
		d := time.Duration(*in.Body.ExpiresInDays) * day
		ttl = &d
	}
	token, secret, err := h.repo.Create(ctx, in.Body.Name, scopes, ttl)
	switch {
	case errors.Is(err, agentaccess.ErrUnknownScope), errors.Is(err, agentaccess.ErrNoScopes):
		return nil, apierr.Unprocessable(apierr.CodeUnknownScope)
	case errors.Is(err, agentaccess.ErrTTLTooLong):
		return nil, apierr.Unprocessable(apierr.CodeTokenTTLTooLong)
	case err != nil:
		return nil, apierr.Internal(ctx, err)
	}
	out := &createAgentTokenOutput{}
	out.Body.AgentToken, out.Body.Secret = toToken(token), secret
	return out, nil
}

func (h *Handler) revoke(ctx context.Context, in *revokeInput) (*struct{}, error) {
	err := h.repo.Revoke(ctx, in.ID)
	if errors.Is(err, agentaccess.ErrNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) audit(ctx context.Context, in *auditInput) (*listAuditOutput, error) {
	var before *int64
	if in.Before > 0 {
		before = &in.Before
	}
	entries, err := h.repo.AuditLog(ctx, auditPageSize, before)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listAuditOutput{}
	out.Body.Entries = make([]AuditEntry, len(entries))
	for i, e := range entries {
		out.Body.Entries[i] = AuditEntry{ID: e.ID, TokenID: e.TokenID, TokenName: e.TokenName, Operation: e.Operation,
			Status: e.Status, ResultCount: e.ResultCount, OccurredAt: e.OccurredAt}
	}
	return out, nil
}

func toToken(t *agentaccess.Token) AgentToken {
	scopes := make([]string, len(t.Scopes))
	for i, s := range t.Scopes {
		scopes[i] = string(s)
	}
	return AgentToken{ID: t.ID, Name: t.Name, Last4: t.Last4, Scopes: scopes, ExpiresAt: t.ExpiresAt, RevokedAt: t.RevokedAt,
		LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt}
}
