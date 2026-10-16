package http

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/identity"
	"coin-book-api/internal/platform/config"
	"coin-book-api/internal/transport/http/accounts"
	"coin-book-api/internal/transport/http/agenttokens"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/auth"
	"coin-book-api/internal/transport/http/budgets"
	"coin-book-api/internal/transport/http/categories"
	"coin-book-api/internal/transport/http/httpapi"
	"coin-book-api/internal/transport/http/imports"
	mw "coin-book-api/internal/transport/http/middleware"
	"coin-book-api/internal/transport/http/reports"
	"coin-book-api/internal/transport/http/transactions"
	"coin-book-api/internal/transport/http/transfers"
)

const apiVersion = "1.0.0"

const requestTimeout = 60 * time.Second

type Deps struct {
	Config   *config.Config
	Sessions *identity.Service
	Tokens   *agentaccess.Repo

	BuildVersion string
	BuildCommit  string

	Auth         *auth.Handler
	Accounts     *accounts.Handler
	Transactions *transactions.Handler
	Imports      *imports.Handler
	Categories   *categories.Handler
	Transfers    *transfers.Handler
	Budgets      *budgets.Handler
	Reports      *reports.Handler
	AgentTokens  *agenttokens.Handler
}

func NewAPI(d Deps) (http.Handler, huma.API) {
	apierr.Install()
	// Handlers always send [] for an empty list, so the contract promises a non-null array.
	huma.DefaultArrayNullable = false

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(mw.RequireAllowedHost(d.Config.Server.AllowedHosts))
	r.Use(mw.SecurityHeaders)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(requestTimeout))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { apierr.Write(w, apierr.NotFound()) })
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		apierr.Write(w, apierr.New(http.StatusMethodNotAllowed, apierr.CodeMethodNotAllowed))
	})

	api := humachi.New(r, apiConfig(d.Config))
	api.UseMiddleware(httpapi.RateLimitMiddleware(api, d.Config.Server.AuthRateLimitPerMinute))
	api.UseMiddleware(httpapi.AuthMiddleware(api, d.Sessions, d.Tokens))

	registerVersion(api, d.BuildVersion, d.BuildCommit)
	d.Auth.Register(api)
	d.Accounts.Register(api)
	d.Transactions.Register(api)
	d.Imports.Register(api)
	d.Categories.Register(api)
	d.Transfers.Register(api)
	d.Budgets.Register(api)
	d.Reports.Register(api)
	d.AgentTokens.Register(api)
	return r, api
}

func apiConfig(cfg *config.Config) huma.Config {
	c := huma.DefaultConfig("coin-book API", apiVersion)
	c.Info.Description = "Personal finance: accounts, imported transactions, categories, transfers, budgets and reports.\n\n" +
		"Amounts are integers in the currency's smallest unit (`minor`) with an ISO 4217 `currency`; " +
		"currencies are never summed together. Dates are calendar days in Europe/Prague.\n\n" +
		"Errors are `application/problem+json` (RFC 9457) with a stable `code`; branch on `code`, not `detail`."
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{httpapi.SecuritySchemeBearer: httpapi.BearerScheme()}
	c.CreateHooks = nil
	c.DocsRenderer = huma.DocsRendererSwaggerUI
	c.DocsPath = "/api/docs"
	c.OpenAPIPath = "/api/openapi"
	c.SchemasPath = "/api/schemas"
	if !cfg.Server.DocsEnabled {
		c.DocsPath, c.OpenAPIPath, c.SchemasPath = "", "", ""
	}
	return c
}

type versionOutput struct {
	Body struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		GoVersion string `json:"go_version"`
	}
}

func registerVersion(api huma.API, version, commit string) {
	if version == "" {
		version = "dev"
	}
	httpapi.Register(api, httpapi.Op{Operation: huma.Operation{
		OperationID: "get-version", Method: http.MethodGet, Path: "/api/v1/version", Tags: []string{"Meta"},
		Summary: "What this process was built from",
	}}, func(context.Context, *struct{}) (*versionOutput, error) {
		out := &versionOutput{}
		out.Body.Version, out.Body.Commit, out.Body.GoVersion = version, commit, runtime.Version()
		return out, nil
	})
}
