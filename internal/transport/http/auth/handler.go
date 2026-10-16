package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"coin-book-api/internal/domain/identity"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/httpapi"
)

const cookiePath = "/api/v1/auth"

type Handler struct {
	sessions     *identity.Service
	cookieSecure bool
}

func NewHandler(sessions *identity.Service, cookieSecure bool) *Handler {
	return &Handler{sessions: sessions, cookieSecure: cookieSecure}
}

type statusOutput struct {
	Body struct {
		NeedsSetup bool `json:"needs_setup" doc:"True until the single user has been created."`
	}
}

type credentialsInput struct {
	Body struct {
		Email    string `json:"email" format:"email" maxLength:"254"`
		Password string `json:"password" maxLength:"256"`
	}
}

type sessionBody struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in" doc:"Seconds until the access token expires."`
	Email       string `json:"email"`
}

type sessionOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      sessionBody
}

type refreshInput struct {
	Refresh string `cookie:"coinbook_refresh"`
}

type logoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type meOutput struct {
	Body struct {
		Email     string    `json:"email"`
		CreatedAt time.Time `json:"created_at"`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Operation: huma.Operation{
		OperationID: "get-auth-status", Method: http.MethodGet, Path: "/api/v1/auth/status", Tags: []string{"Auth"},
		Summary: "Whether the user still has to be created",
	}}, h.status)
	httpapi.Register(api, httpapi.Op{RateLimit: true, Operation: huma.Operation{
		OperationID: "setup", Method: http.MethodPost, Path: "/api/v1/auth/setup", Tags: []string{"Auth"},
		Summary: "Create the single user and sign in", Errors: []int{409, 422},
	}}, h.setup)
	httpapi.Register(api, httpapi.Op{RateLimit: true, Operation: huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/api/v1/auth/login", Tags: []string{"Auth"},
		Summary: "Sign in", Errors: []int{401},
	}}, h.login)
	httpapi.Register(api, httpapi.Op{Operation: huma.Operation{
		OperationID: "refresh-session", Method: http.MethodPost, Path: "/api/v1/auth/refresh", Tags: []string{"Auth"},
		Summary: "Exchange the refresh cookie for a new access token", Errors: []int{401},
	}}, h.refresh)
	httpapi.Register(api, httpapi.Op{Operation: huma.Operation{
		OperationID: "logout", Method: http.MethodPost, Path: "/api/v1/auth/logout", Tags: []string{"Auth"},
		Summary: "Clear the refresh cookie", DefaultStatus: http.StatusNoContent,
	}}, h.logout)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "get-me", Method: http.MethodGet, Path: "/api/v1/me", Tags: []string{"Auth"},
		Summary: "The signed-in user",
	}}, h.me)
}

func (h *Handler) status(ctx context.Context, _ *struct{}) (*statusOutput, error) {
	needs, err := h.sessions.NeedsSetup(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &statusOutput{}
	out.Body.NeedsSetup = needs
	return out, nil
}

func (h *Handler) setup(ctx context.Context, in *credentialsInput) (*sessionOutput, error) {
	u, err := h.sessions.Setup(ctx, in.Body.Email, in.Body.Password)
	switch {
	case errors.Is(err, identity.ErrAlreadySetUp):
		return nil, apierr.Conflict(apierr.CodeAlreadySetUp)
	case errors.Is(err, identity.ErrPasswordTooShort):
		return nil, apierr.Unprocessable(apierr.CodePasswordTooShort)
	case errors.Is(err, identity.ErrInvalidCredentials):
		return nil, apierr.Validation("body.email", "expected an email address")
	case err != nil:
		return nil, apierr.Internal(ctx, err)
	}
	return h.session(ctx, u)
}

func (h *Handler) login(ctx context.Context, in *credentialsInput) (*sessionOutput, error) {
	u, err := h.sessions.Login(ctx, in.Body.Email, in.Body.Password)
	if errors.Is(err, identity.ErrInvalidCredentials) {
		return nil, apierr.New(http.StatusUnauthorized, apierr.CodeInvalidCredentials)
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return h.session(ctx, u)
}

func (h *Handler) refresh(ctx context.Context, in *refreshInput) (*sessionOutput, error) {
	claims, err := h.sessions.Parse(in.Refresh, identity.RefreshToken)
	if err != nil {
		return nil, apierr.Unauthorized()
	}
	u, err := h.sessions.Get(ctx, claims.UserID)
	if err != nil {
		return nil, apierr.Unauthorized()
	}
	return h.session(ctx, u)
}

func (h *Handler) logout(context.Context, *struct{}) (*logoutOutput, error) {
	return &logoutOutput{SetCookie: h.cookie("", -1)}, nil
}

func (h *Handler) me(ctx context.Context, _ *struct{}) (*meOutput, error) {
	id, _ := httpapi.UserID(ctx)
	u, err := h.sessions.Get(ctx, id)
	if err != nil {
		return nil, apierr.Unauthorized()
	}
	out := &meOutput{}
	out.Body.Email, out.Body.CreatedAt = u.Email, u.CreatedAt
	return out, nil
}

func (h *Handler) session(ctx context.Context, u *identity.User) (*sessionOutput, error) {
	access, err := h.sessions.Mint(u.ID, identity.AccessToken)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	refresh, err := h.sessions.Mint(u.ID, identity.RefreshToken)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &sessionOutput{
		SetCookie: h.cookie(refresh, int(h.sessions.RefreshTTL().Seconds())),
		Body:      sessionBody{AccessToken: access, ExpiresIn: int(h.sessions.AccessTTL().Seconds()), Email: u.Email},
	}, nil
}

func (h *Handler) cookie(value string, maxAge int) http.Cookie {
	return http.Cookie{ //nolint:gosec // G124: Secure follows config; plain http on localhost would drop a Secure cookie
		Name: identity.RefreshCookieName, Value: value, Path: cookiePath, MaxAge: maxAge,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	}
}
