//go:build integration

package acceptance

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/platform/config"
)

const googleClientID = "test-client"

// fakeGoogle is an OpenID Connect issuer that hands out an id token for whatever
// identity the test sets, so the API's side of the flow runs for real.
type fakeGoogle struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	nonce    string
	subject  string
	email    string
	verified bool
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	g := &fakeGoogle{key: key, verified: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": g.server.URL, "authorization_endpoint": g.server.URL + "/auth", "token_endpoint": g.server.URL + "/token",
			"jwks_uri": g.server.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, r.ParseForm())
		if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		claims := jwt.MapClaims{
			"iss": g.server.URL, "aud": googleClientID, "sub": g.subject, "email": g.email, "email_verified": g.verified,
			"nonce": g.nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "test"
		signed, err := token.SignedString(key)
		require.NoError(t, err)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": signed})
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

func newGoogleServer(t *testing.T) (*client, *fakeGoogle) {
	t.Helper()
	google := newFakeGoogle(t)
	c := newServer(t, func(cfg *config.Config) {
		cfg.Server.PublicURL = "http://app.test"
		cfg.Google = config.GoogleConfig{ClientID: googleClientID, ClientSecret: "secret", IssuerURL: google.server.URL}
	})
	return c, google
}

// redirect is what the browser would get from an endpoint that answers with a redirect.
type redirect struct {
	Status   int
	Location string
	Cookies  []*http.Cookie
	header   http.Header
}

func (r redirect) refreshCookie() *http.Cookie {
	for _, ck := range r.Cookies {
		if ck.Name == "coinbook_refresh" && ck.Value != "" {
			return ck
		}
	}
	return nil
}

// startGoogle plays the browser up to the point where Google redirects back: it returns
// the state Google would echo and the handshake cookie the browser would carry.
func (c *client) startGoogle(google *fakeGoogle) (state string, cookie *http.Cookie) {
	c.t.Helper()
	resp := c.getNoRedirect("/api/v1/auth/google", nil)
	require.Equal(c.t, http.StatusFound, resp.Status)
	location, err := url.Parse(resp.Location)
	require.NoError(c.t, err)
	assert.Equal(c.t, google.server.URL+"/auth", location.Scheme+"://"+location.Host+location.Path)
	assert.Equal(c.t, "http://app.test/api/v1/auth/google/callback", location.Query().Get("redirect_uri"))
	google.nonce = location.Query().Get("nonce")
	for _, ck := range resp.Cookies {
		if ck.Name == "coinbook_google" {
			cookie = ck
		}
	}
	require.NotNil(c.t, cookie)
	return location.Query().Get("state"), cookie
}

func (c *client) finishGoogle(state string, cookie *http.Cookie) redirect {
	c.t.Helper()
	return c.getNoRedirect("/api/v1/auth/google/callback?code=good-code&state="+url.QueryEscape(state), cookie)
}

func (c *client) getNoRedirect(path string, cookie *http.Cookie) redirect {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.server.URL+path, nil)
	require.NoError(c.t, err)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	return redirect{Status: resp.StatusCode, Location: resp.Header.Get("Location"), Cookies: resp.Cookies(), header: resp.Header}
}

func (c *client) sessionFromCookie(cookie *http.Cookie) *client {
	c.t.Helper()
	require.NotNil(c.t, cookie, "the callback set no refresh cookie")
	req, err := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/auth/refresh", nil)
	require.NoError(c.t, err)
	req.AddCookie(cookie)
	var session struct {
		AccessToken string `json:"access_token"`
	}
	c.withToken("").send(req, http.StatusOK, &session)
	return c.withToken(session.AccessToken)
}

func TestGoogleSignInCreatesAUserAndASession(t *testing.T) {
	c, google := newGoogleServer(t)
	google.subject, google.email = "sub-1", "Nova@Example.com"

	state, cookie := c.startGoogle(google)
	resp := c.finishGoogle(state, cookie)
	require.Equal(t, http.StatusFound, resp.Status)
	assert.Equal(t, "http://app.test/", resp.Location)
	refresh := resp.refreshCookie()
	require.NotNil(t, refresh)

	var me struct {
		Email string `json:"email"`
	}
	c.sessionFromCookie(refresh).do(http.MethodGet, "/api/v1/me", nil, http.StatusOK, &me)
	assert.Equal(t, "nova@example.com", me.Email)

	// Signing in again with the same Google account is the same user, not a second one.
	state, cookie = c.startGoogle(google)
	again := c.sessionFromCookie(c.finishGoogle(state, cookie).refreshCookie())
	var status struct {
		Google bool `json:"google"`
	}
	again.do(http.MethodGet, "/api/v1/auth/status", nil, http.StatusOK, &status)
	assert.True(t, status.Google)
	var count int
	require.NoError(t, pg.Pool.QueryRow(t.Context(), `SELECT count(*) FROM app_user`).Scan(&count))
	assert.Equal(t, 1, count)
}

// Registration never verifies the email, so a password account with the same address
// may belong to anyone; the Google account must not be attached to it.
func TestGoogleSignInRefusesAnEmailRegisteredWithAPassword(t *testing.T) {
	c, google := newGoogleServer(t)
	alice := c.signUp("alice@coinbook.local")
	alice.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	google.subject, google.email = "sub-alice", "alice@coinbook.local"

	state, cookie := c.startGoogle(google)
	resp := c.finishGoogle(state, cookie)
	assert.Equal(t, "http://app.test/login?error=email_taken", resp.Location)
	assert.Nil(t, resp.refreshCookie())

	google.verified = false
	google.subject, google.email = "sub-stranger", "bob@coinbook.local"
	state, cookie = c.startGoogle(google)
	resp = c.finishGoogle(state, cookie)
	assert.Equal(t, "http://app.test/login?error=email_not_verified", resp.Location)
	assert.Nil(t, resp.refreshCookie())
}

func TestGoogleCallbackRefusesAForgedState(t *testing.T) {
	c, google := newGoogleServer(t)
	google.subject, google.email = "sub-1", "nova@example.com"
	_, cookie := c.startGoogle(google)

	resp := c.finishGoogle("forged", cookie)
	assert.Equal(t, "http://app.test/login?error=google_sign_in_failed", resp.Location)
	assert.Nil(t, resp.refreshCookie())
	assert.True(t, strings.Contains(resp.header.Get("Set-Cookie"), "coinbook_google=;"), "the handshake cookie is cleared")

	resp = c.getNoRedirect("/api/v1/auth/google/callback?code=good-code&state=x", nil)
	assert.Equal(t, "http://app.test/login?error=google_sign_in_failed", resp.Location)
}

func TestGoogleSignInIsHiddenWhenNotConfigured(t *testing.T) {
	c := newClient(t)
	var status struct {
		Google bool `json:"google"`
	}
	c.do(http.MethodGet, "/api/v1/auth/status", nil, http.StatusOK, &status)
	assert.False(t, status.Google)
	c.do(http.MethodGet, "/api/v1/auth/google", nil, http.StatusNotFound, nil)
}
