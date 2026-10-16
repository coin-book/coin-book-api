// Package app wires the dependency graph; shared by the server and the OpenAPI generator.
package app

import (
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/budget"
	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/identity"
	"coin-book-api/internal/domain/importing"
	"coin-book-api/internal/domain/report"
	"coin-book-api/internal/domain/spending"
	"coin-book-api/internal/domain/transaction"
	"coin-book-api/internal/domain/transfer"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/config"
	httppkg "coin-book-api/internal/transport/http"
	"coin-book-api/internal/transport/http/accounts"
	"coin-book-api/internal/transport/http/agenttokens"
	"coin-book-api/internal/transport/http/auth"
	"coin-book-api/internal/transport/http/budgets"
	"coin-book-api/internal/transport/http/categories"
	"coin-book-api/internal/transport/http/imports"
	"coin-book-api/internal/transport/http/reports"
	"coin-book-api/internal/transport/http/transactions"
	"coin-book-api/internal/transport/http/transfers"
)

type Build struct {
	Version string
	Commit  string
}

// New builds the API. A nil pool is allowed for generating the OpenAPI document,
// which only needs the operations registered, never executed.
func New(cfg *config.Config, pool *pgxpool.Pool, c clock.Clock, build Build) (http.Handler, huma.API) {
	sessions := identity.NewService(pool, cfg.JWT.Secret,
		time.Duration(cfg.JWT.AccessTokenMinutes)*time.Minute, time.Duration(cfg.JWT.RefreshTokenHours)*time.Hour)
	tokens := agentaccess.NewRepo(pool)
	accountRepo := account.NewRepo(pool)
	transactionRepo := transaction.NewRepo(pool)
	categoryRepo := categorization.NewRepo(pool)
	spendingRepo := spending.NewRepo(pool)

	categorizer := categorization.NewService(pool, categoryRepo)
	transferService := transfer.NewService(pool, categorizer)
	importer := importing.NewService(pool, accountRepo, categorizer, transferService, cfg.Storage.ImportsDir)

	return httppkg.NewAPI(httppkg.Deps{
		Config: cfg, Sessions: sessions, Tokens: tokens,
		BuildVersion: build.Version, BuildCommit: build.Commit,
		Auth:         auth.NewHandler(sessions, cfg.Server.CookieSecure),
		Accounts:     accounts.NewHandler(accountRepo),
		Transactions: transactions.NewHandler(transactionRepo, categorizer),
		Imports:      imports.NewHandler(importer, cfg.Server.MaxUploadBytes()),
		Categories:   categories.NewHandler(categoryRepo),
		Transfers:    transfers.NewHandler(transferService),
		Budgets:      budgets.NewHandler(budget.NewService(pool, spendingRepo, categoryRepo, c)),
		Reports:      reports.NewHandler(report.NewService(spendingRepo)),
		AgentTokens:  agenttokens.NewHandler(tokens),
	})
}
