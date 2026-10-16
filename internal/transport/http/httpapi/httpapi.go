// Package httpapi registers operations with the access tier they need, so the tier
// is declared once and drives the middleware, the OpenAPI security block and the
// documented error responses together.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/identity"
	"coin-book-api/internal/transport/http/apierr"
)

const SecuritySchemeBearer = "bearerAuth"

type Auth int

const (
	AuthNone Auth = iota
	// AuthSession is the signed-in user only; an agent token is refused. Every write is here (F-AGT-2).
	AuthSession
	// AuthRead admits the session or an agent token carrying Op.Scope.
	AuthRead
)

const (
	metaAuth      = "coinbook:auth"
	metaScope     = "coinbook:scope"
	metaRateLimit = "coinbook:ratelimit"
)

type Op struct {
	huma.Operation
	Auth      Auth
	Scope     agentaccess.Scope
	RateLimit bool
}

func Register[I, O any](api huma.API, op Op, handler func(context.Context, *I) (*O, error)) {
	o := op.Operation
	if o.Metadata == nil {
		o.Metadata = map[string]any{}
	}
	if op.Auth == AuthRead && op.Scope == "" {
		panic("httpapi: AuthRead needs a scope: " + o.OperationID)
	}
	o.Metadata[metaAuth] = op.Auth
	o.Metadata[metaScope] = op.Scope
	if op.RateLimit {
		o.Metadata[metaRateLimit] = true
		o.Errors = append(o.Errors, http.StatusTooManyRequests)
	}
	if op.Auth != AuthNone {
		o.Security = []map[string][]string{{SecuritySchemeBearer: {}}}
		o.Errors = append(o.Errors, http.StatusUnauthorized, http.StatusForbidden)
	}
	if op.Auth == AuthRead {
		o.Description = strings.TrimSpace(o.Description + "\n\nAgent tokens need the `" + string(op.Scope) + "` scope.")
	}
	o.Errors = append(o.Errors, http.StatusInternalServerError)
	huma.Register(api, o, handler)
}

func BearerScheme() *huma.SecurityScheme {
	return &huma.SecurityScheme{
		Type:   "http",
		Scheme: "bearer",
		Description: "Either the session access token from `POST /api/v1/auth/login` (a short-lived JWT, " +
			"refreshed with the HttpOnly refresh cookie) or an agent token `coinbook_pat_…` from " +
			"`POST /api/v1/agent-tokens`. Agent tokens are read-only and limited to their scopes; every " +
			"request made with one is written to the audit log.",
	}
}

type ctxUserKey struct{}

type ctxTokenKey struct{}

type ctxResultCountKey struct{}

func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(ctxUserKey{}).(uuid.UUID)
	return id, ok
}

// CountResults records how many records a read returned, for the audit log.
func CountResults(ctx context.Context, n int) {
	if counter, ok := ctx.Value(ctxResultCountKey{}).(*int); ok {
		*counter = n
	}
}

func authOf(op *huma.Operation) (Auth, agentaccess.Scope) {
	if op == nil {
		return AuthNone, ""
	}
	level, _ := op.Metadata[metaAuth].(Auth)
	scope, _ := op.Metadata[metaScope].(agentaccess.Scope)
	return level, scope
}

// AuthMiddleware enforces the declared tier and audits every agent-token request (F-AGT-2).
func AuthMiddleware(api huma.API, sessions *identity.Service, tokens *agentaccess.Repo) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		level, scope := authOf(ctx.Operation())
		if level == AuthNone {
			next(ctx)
			return
		}
		raw, ok := bearerToken(ctx.Header("Authorization"))
		if !ok {
			writeErr(api, ctx, apierr.Unauthorized())
			return
		}
		if !agentaccess.Looks(raw) {
			claims, err := sessions.Parse(raw, identity.AccessToken)
			if err != nil {
				writeErr(api, ctx, apierr.Unauthorized())
				return
			}
			next(huma.WithValue(ctx, ctxUserKey{}, claims.UserID))
			return
		}

		token, err := tokens.Authenticate(ctx.Context(), raw)
		if err != nil {
			writeErr(api, ctx, apierr.Unauthorized())
			return
		}
		var count *int
		counter := 0
		ctx = huma.WithValue(huma.WithValue(ctx, ctxTokenKey{}, token), ctxResultCountKey{}, &counter)
		switch {
		case level == AuthSession:
			writeErr(api, ctx, apierr.Forbidden(apierr.CodeSessionRequired))
		case !token.Allows(scope):
			writeErr(api, ctx, apierr.Forbidden(apierr.CodeInsufficientScope))
		default:
			next(ctx)
			count = &counter
		}
		if err := tokens.RecordAccess(ctx.Context(), token.ID, ctx.Operation().OperationID, ctx.Status(), count); err != nil {
			slog.ErrorContext(ctx.Context(), "record agent access", "err", err)
		}
	}
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func writeErr(api huma.API, ctx huma.Context, p *apierr.Problem) {
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(p.Status)
	if err := api.Marshal(ctx.BodyWriter(), "application/json", p); err != nil {
		slog.Error("write error response", "err", err)
	}
}
