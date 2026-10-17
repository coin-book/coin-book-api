package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"coin-book-api/internal/domain/identity"
	"coin-book-api/internal/platform/config"
	"coin-book-api/internal/platform/googleauth"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/httpapi"
)

const (
	cookiePath         = "/api/v1/auth"
	googlePath         = "/api/v1/auth/google"
	GoogleCallbackPath = "/api/v1/auth/google/callback"
	handshakeCookie    = "coinbook_google"
	handshakeTTL       = 10 * time.Minute
	loginPath          = "/login"
)

type Handler struct {
	sessions *identity.Service
	google   *googleauth.Client
	server   config.ServerConfig
}

func NewHandler(sessions *identity.Service, google *googleauth.Client, server config.ServerConfig) *Handler {
	return &Handler{sessions: sessions, google: google, server: server}
}

type statusOutput struct {
	Body struct {
		Google bool `json:"google" doc:"Whether signing in with Google is available."`
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

type googleStartOutput struct {
	Status    int
	Location  string      `header:"Location"`
	SetCookie http.Cookie `header:"Set-Cookie"`
}

type googleCallbackInput struct {
	Code      string `query:"code"`
	State     string `query:"state"`
	Error     string `query:"error" doc:"Set by Google when the user did not grant access."`
	Handshake string `cookie:"coinbook_google"`
}

type googleCallbackOutput struct {
	Status    int
	Location  string        `header:"Location"`
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Operation: huma.Operation{
		OperationID: "get-auth-status", Method: http.MethodGet, Path: "/api/v1/auth/status", Tags: []string{"Auth"},
		Summary: "Which ways of signing in are available",
	}}, h.status)
	httpapi.Register(api, httpapi.Op{RateLimit: true, Operation: huma.Operation{
		OperationID: "register", Method: http.MethodPost, Path: "/api/v1/auth/register", Tags: []string{"Auth"},
		Summary: "Create a user and sign in", Errors: []int{409, 422},
	}}, h.register)
	httpapi.Register(api, httpapi.Op{RateLimit: true, Operation: huma.Operation{
		OperationID: "login", Method: http.MethodPost, Path: "/api/v1/auth/login", Tags: []string{"Auth"},
		Summary: "Sign in", Errors: []int{401},
	}}, h.login)
	httpapi.Register(api, httpapi.Op{RateLimit: true, Operation: huma.Operation{
		OperationID: "google-sign-in", Method: http.MethodGet, Path: googlePath, Tags: []string{"Auth"},
		Summary: "Send the browser to Google to sign in", DefaultStatus: http.StatusFound, Errors: []int{404},
		Description: "Open this in the browser, not with fetch: Google sends the user back to the callback, " +
			"which sets the refresh cookie and redirects to the app.",
	}}, h.googleStart)
	httpapi.Register(api, httpapi.Op{RateLimit: true, Operation: huma.Operation{
		OperationID: "google-callback", Method: http.MethodGet, Path: GoogleCallbackPath, Tags: []string{"Auth"},
		Summary: "Where Google sends the browser back", DefaultStatus: http.StatusFound,
		Description: "Redirects to the app with the session cookie set, or to the sign-in page with `error` " +
			"naming what went wrong (`google_sign_in_failed`, `email_not_verified`, `email_taken`).",
	}}, h.googleCallback)
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

func (h *Handler) status(context.Context, *struct{}) (*statusOutput, error) {
	out := &statusOutput{}
	out.Body.Google = h.google.Enabled()
	return out, nil
}

func (h *Handler) register(ctx context.Context, in *credentialsInput) (*sessionOutput, error) {
	u, err := h.sessions.Register(ctx, in.Body.Email, in.Body.Password)
	switch {
	case errors.Is(err, identity.ErrEmailTaken):
		return nil, apierr.Conflict(apierr.CodeEmailTaken)
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

func (h *Handler) googleStart(ctx context.Context, _ *struct{}) (*googleStartOutput, error) {
	if !h.google.Enabled() {
		return nil, apierr.NotFound()
	}
	handshake, err := googleauth.NewHandshake()
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	location, err := h.google.AuthURL(ctx, handshake)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &googleStartOutput{
		Status: http.StatusFound, Location: location,
		SetCookie: h.handshakeCookie(strings.Join([]string{handshake.State, handshake.Nonce, handshake.Verifier}, "."), int(handshakeTTL.Seconds())),
	}, nil
}

func (h *Handler) googleCallback(ctx context.Context, in *googleCallbackInput) (*googleCallbackOutput, error) {
	u, code := h.finishGoogle(ctx, in)
	out := &googleCallbackOutput{Status: http.StatusFound, SetCookie: []http.Cookie{h.handshakeCookie("", -1)}}
	if code != "" {
		out.Location = h.server.PublicURL + loginPath + "?error=" + string(code)
		return out, nil
	}
	refresh, err := h.sessions.Mint(u.ID, identity.RefreshToken)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out.SetCookie = append(out.SetCookie, h.cookie(refresh, int(h.sessions.RefreshTTL().Seconds())))
	out.Location = h.server.PublicURL + "/"
	return out, nil
}

// finishGoogle returns the signed-in user, or the error code the sign-in page shows.
func (h *Handler) finishGoogle(ctx context.Context, in *googleCallbackInput) (*identity.User, apierr.Code) {
	parts := strings.Split(in.Handshake, ".")
	if !h.google.Enabled() || len(parts) != 3 || slices.Contains(parts, "") || in.Error != "" || in.Code == "" ||
		subtle.ConstantTimeCompare([]byte(parts[0]), []byte(in.State)) != 1 {
		return nil, apierr.CodeGoogleSignInFailed
	}
	handshake := googleauth.Handshake{State: parts[0], Nonce: parts[1], Verifier: parts[2]}
	id, err := h.google.Exchange(ctx, in.Code, handshake)
	if err != nil {
		slog.WarnContext(ctx, "google sign-in failed", "err", err)
		return nil, apierr.CodeGoogleSignInFailed
	}
	u, err := h.sessions.SignInWithGoogle(ctx, identity.ExternalIdentity{Subject: id.Subject, Email: id.Email, EmailVerified: id.EmailVerified})
	switch {
	case errors.Is(err, identity.ErrEmailNotVerified):
		return nil, apierr.CodeEmailNotVerified
	case errors.Is(err, identity.ErrEmailTaken):
		return nil, apierr.CodeEmailTaken
	case err != nil:
		slog.ErrorContext(ctx, "google sign-in", "err", err)
		return nil, apierr.CodeGoogleSignInFailed
	}
	return u, ""
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
		HttpOnly: true, Secure: h.server.CookieSecure, SameSite: http.SameSiteStrictMode,
	}
}

// The handshake cookie is Lax, not Strict: Google sends the browser back with a
// top-level cross-site navigation, which does not carry Strict cookies.
func (h *Handler) handshakeCookie(value string, maxAge int) http.Cookie {
	return http.Cookie{ //nolint:gosec // G124: Secure follows config, as for the refresh cookie
		Name: handshakeCookie, Value: value, Path: googlePath, MaxAge: maxAge,
		HttpOnly: true, Secure: h.server.CookieSecure, SameSite: http.SameSiteLaxMode,
	}
}
