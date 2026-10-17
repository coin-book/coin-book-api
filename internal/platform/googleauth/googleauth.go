// Package googleauth runs the OpenID Connect authorization-code flow against Google
// (or any issuer that follows the same discovery document, which the tests use).
package googleauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/rotisserie/eris"
	"golang.org/x/oauth2"

	"coin-book-api/internal/platform/config"
)

const discoveryTimeout = 10 * time.Second

type Client struct {
	cfg         config.GoogleConfig
	redirectURL string

	mu       sync.Mutex
	provider *oidc.Provider
}

// New does no network I/O: the issuer is discovered on first use, so the API starts
// without Google being reachable.
func New(cfg config.GoogleConfig, redirectURL string) *Client {
	return &Client{cfg: cfg, redirectURL: redirectURL}
}

func (c *Client) Enabled() bool { return c.cfg.Enabled() }

type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
}

// Handshake is what the start of a sign-in leaves with the browser, to be checked when
// Google sends it back.
type Handshake struct {
	State    string
	Nonce    string
	Verifier string
}

func NewHandshake() (Handshake, error) {
	state, err := randomToken()
	if err != nil {
		return Handshake{}, err
	}
	nonce, err := randomToken()
	if err != nil {
		return Handshake{}, err
	}
	return Handshake{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier()}, nil
}

func (c *Client) AuthURL(ctx context.Context, h Handshake) (string, error) {
	oauth, _, err := c.setup(ctx)
	if err != nil {
		return "", err
	}
	return oauth.AuthCodeURL(h.State, oidc.Nonce(h.Nonce), oauth2.S256ChallengeOption(h.Verifier)), nil
}

// Exchange turns the code Google sent back into a verified identity.
func (c *Client) Exchange(ctx context.Context, code string, h Handshake) (*Identity, error) {
	oauth, verifier, err := c.setup(ctx)
	if err != nil {
		return nil, err
	}
	token, err := oauth.Exchange(ctx, code, oauth2.VerifierOption(h.Verifier))
	if err != nil {
		return nil, eris.Wrap(err, "exchange authorization code")
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, eris.New("token response carries no id_token")
	}
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, eris.Wrap(err, "verify id token")
	}
	if idToken.Nonce != h.Nonce {
		return nil, eris.New("id token nonce does not match")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, eris.Wrap(err, "read id token claims")
	}
	return &Identity{Subject: idToken.Subject, Email: claims.Email, EmailVerified: claims.EmailVerified}, nil
}

func (c *Client) setup(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	provider, err := c.discover(ctx)
	if err != nil {
		return nil, nil, err
	}
	oauth := &oauth2.Config{
		ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret, RedirectURL: c.redirectURL,
		Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email"},
	}
	return oauth, provider.Verifier(&oidc.Config{ClientID: c.cfg.ClientID}), nil
}

// discover fetches the issuer's configuration once; a failed attempt (an outage) is
// retried by the next sign-in rather than remembered.
func (c *Client) discover(ctx context.Context) (*oidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider != nil {
		return c.provider, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
	defer cancel()
	provider, err := oidc.NewProvider(ctx, c.cfg.IssuerURL)
	if err != nil {
		return nil, eris.Wrap(err, "discover issuer")
	}
	c.provider = provider
	return provider, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", eris.Wrap(err, "generate random token")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
