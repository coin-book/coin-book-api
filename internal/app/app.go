// Package app wires the dependency graph; shared by the server and the OpenAPI generator.
package app

import (
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/bank"
	"coin-book-api/internal/domain/budget"
	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/identity"
	"coin-book-api/internal/domain/importing"
	"coin-book-api/internal/domain/investment"
	"coin-book-api/internal/domain/report"
	"coin-book-api/internal/domain/settings"
	"coin-book-api/internal/domain/spending"
	"coin-book-api/internal/domain/transaction"
	"coin-book-api/internal/domain/transfer"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/config"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/googleauth"
	"coin-book-api/internal/platform/marketdata"
	httppkg "coin-book-api/internal/transport/http"
	"coin-book-api/internal/transport/http/accounts"
	"coin-book-api/internal/transport/http/agenttokens"
	"coin-book-api/internal/transport/http/auth"
	"coin-book-api/internal/transport/http/budgets"
	"coin-book-api/internal/transport/http/categories"
	"coin-book-api/internal/transport/http/imports"
	"coin-book-api/internal/transport/http/investments"
	"coin-book-api/internal/transport/http/reports"
	settingshttp "coin-book-api/internal/transport/http/settings"
	"coin-book-api/internal/transport/http/transactions"
	"coin-book-api/internal/transport/http/transfers"
)

type Build struct {
	Version string
	Commit  string
}

type App struct {
	Handler http.Handler
	API     huma.API
	// Prices runs in the background for the server's lifetime; see investment.Refresher.Run.
	Prices *investment.Refresher
}

// New builds the API. A nil pool is allowed for generating the OpenAPI document,
// which only needs the operations registered, never executed.
func New(cfg *config.Config, pool *db.Pool, c clock.Clock, build Build) *App {
	sessions := identity.NewService(pool, cfg.JWT.Secret,
		time.Duration(cfg.JWT.AccessTokenMinutes)*time.Minute, time.Duration(cfg.JWT.RefreshTokenHours)*time.Hour)
	tokens := agentaccess.NewRepo(pool)
	accountRepo := account.NewRepo(pool)
	transactionRepo := transaction.NewRepo(pool)
	categoryRepo := categorization.NewRepo(pool)
	spendingRepo := spending.NewRepo(pool)

	settingsRepo := settings.NewRepo(pool)
	categorizer := categorization.NewService(pool, categoryRepo, settingsRepo)
	transferService := transfer.NewService(pool, categorizer)
	prices := newPriceRefresher(cfg.Market, pool, c)
	importer := importing.NewService(pool, accountRepo, categorizer, transferService, prices, cfg.Storage.ImportsDir)
	google := googleauth.New(cfg.Google, cfg.Server.PublicURL+auth.GoogleCallbackPath)

	investmentService := investment.NewService(investment.NewRepo(pool), c)
	handler, api := httppkg.NewAPI(httppkg.Deps{
		Config: cfg, Pool: pool, Sessions: sessions, Tokens: tokens,
		BuildVersion: build.Version, BuildCommit: build.Commit,
		Auth:         auth.NewHandler(sessions, google, cfg.Server),
		Accounts:     accounts.NewHandler(accountRepo, bank.NewRepo(pool), transferService, pool),
		Transactions: transactions.NewHandler(transactionRepo, categorizer),
		Imports:      imports.NewHandler(importer, cfg.Server.MaxUploadBytes()),
		Categories:   categories.NewHandler(categoryRepo),
		Transfers:    transfers.NewHandler(transferService),
		Budgets:      budgets.NewHandler(budget.NewService(pool, spendingRepo, categoryRepo, c)),
		Reports:      reports.NewHandler(report.NewService(spendingRepo), accountRepo, investmentService),
		AgentTokens:  agenttokens.NewHandler(tokens),
		Settings:     settingshttp.NewHandler(settingsRepo, categorizer),
		Investments:  investments.NewHandler(investmentService, prices, c),
	})
	return &App{Handler: handler, API: api, Prices: prices}
}

func newPriceRefresher(cfg config.MarketConfig, pool *db.Pool, c clock.Clock) *investment.Refresher {
	if !cfg.Enabled {
		return investment.NewRefresher(pool, nil, nil, c, cfg.RefreshAtHour)
	}
	return investment.NewRefresher(pool, marketdata.NewYahoo(cfg.YahooURL, cfg.UserAgent),
		marketdata.NewCNB(cfg.CNBURL, cfg.UserAgent), c, cfg.RefreshAtHour)
}
