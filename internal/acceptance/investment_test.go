//go:build integration

package acceptance

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"coin-book-api/internal/platform/config"
)

type portfolio struct {
	Positions []struct {
		Instrument struct {
			ISIN        string  `json:"isin"`
			Ticker      string  `json:"ticker"`
			YahooSymbol *string `json:"yahoo_symbol"`
		} `json:"instrument"`
		Quantity string `json:"quantity"`
		Lots     []struct {
			BoughtOn   string `json:"bought_on"`
			ExemptFrom string `json:"exempt_from"`
		} `json:"lots"`
		Value *struct {
			Minor    int64  `json:"minor"`
			Currency string `json:"currency"`
		} `json:"value"`
		Trend    []string `json:"trend"`
		ValueCZK *struct {
			Minor int64 `json:"minor"`
		} `json:"value_czk"`
		IncompleteHistory bool `json:"incomplete_history"`
	} `json:"positions"`
	IncompleteHistory []struct {
		ISIN string `json:"isin"`
	} `json:"incomplete_history"`
	RatesOn *string `json:"rates_on"`
	Prices  struct {
		Enabled bool `json:"enabled"`
	} `json:"prices"`
}

// fakeMarket serves a Yahoo chart for AAPL and EUNL.DE and a ČNB fixing.
func fakeMarket(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/cnb":
			_, _ = w.Write([]byte("16.10.2026 #200\nzemě|měna|množství|kód|kurz\nEMU|euro|1|EUR|25,000\nUSA|dolar|1|USD|20,000\n"))
		case strings.HasSuffix(r.URL.Path, "/AAPL"):
			_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"currency":"USD","gmtoffset":-14400,"longName":"Apple Inc.",
				"fullExchangeName":"NasdaqGS","instrumentType":"EQUITY","fiftyTwoWeekLow":243.42,"fiftyTwoWeekHigh":348.15},
				"timestamp":[1792157400],
				"indicators":{"quote":[{"open":[299],"high":[301],"low":[298],"close":[300],"volume":[1000]}]}}]}}`))
		case strings.HasSuffix(r.URL.Path, "/EUNL.DE"):
			_, _ = w.Write([]byte(`{"chart":{"result":[{"meta":{"currency":"EUR","gmtoffset":7200},"timestamp":[1792134000],
				"indicators":{"quote":[{"open":[null],"high":[null],"low":[null],"close":[100],"volume":[null]}]}}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newMarketClient(t *testing.T) *client {
	t.Helper()
	market := fakeMarket(t)
	return newServer(t, func(cfg *config.Config) {
		cfg.Market = config.MarketConfig{Enabled: true, YahooURL: market.URL, CNBURL: market.URL + "/cnb", UserAgent: "test", RefreshAtHour: 23}
	}).signUp("investor@coinbook.local")
}

func investmentStatement(t *testing.T) []byte {
	t.Helper()
	content, err := os.ReadFile("../domain/importing/parser/revolut/testdata/investments.csv")
	require.NoError(t, err)
	return content
}

func TestRevolutInvestmentsBecomePositionsValuedAtMarketPrices(t *testing.T) {
	c := newMarketClient(t)
	var imported importResult
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, &imported)
	require.Len(t, imported.Accounts, 2)
	assert.Equal(t, 8, imported.RowsNew, "3 buys, 2 dividends and 3 sold lots")

	var before portfolio
	c.do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, &before)
	require.Len(t, before.Positions, 2)
	assert.Nil(t, before.Positions[0].Value, "no price fetched yet")
	assert.True(t, before.Prices.Enabled)

	assert.Zero(t, c.prices.Refresh(context.Background()))

	var after portfolio
	c.do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, &after)
	require.Len(t, after.Positions, 2)
	aapl, eunl := after.Positions[0], after.Positions[1]
	assert.Equal(t, "AAPL", aapl.Instrument.Ticker)
	assert.Equal(t, "5", aapl.Quantity, "two buys of 3 on 15 Jan, one share of them sold")
	assert.True(t, aapl.IncompleteHistory, "two shares sold from a 2022 purchase that was never imported")
	require.NotNil(t, aapl.Value)
	assert.Equal(t, int64(150000), aapl.Value.Minor)
	assert.Equal(t, []string{"300"}, aapl.Trend)
	assert.Equal(t, int64(3000000), aapl.ValueCZK.Minor)
	require.Len(t, aapl.Lots, 1)
	assert.Equal(t, "2029-01-16", aapl.Lots[0].ExemptFrom)

	assert.Equal(t, "EUNL.DE", *eunl.Instrument.YahooSymbol)
	assert.Equal(t, "1.37654322", eunl.Quantity)
	assert.Equal(t, int64(13765), eunl.Value.Minor)
	assert.Equal(t, "2026-10-16", *after.RatesOn)
	require.Len(t, after.IncompleteHistory, 1)

	var candles struct {
		Symbol  string `json:"symbol"`
		Candles []struct {
			Day   string  `json:"day"`
			Open  *string `json:"open"`
			Close string  `json:"close"`
		} `json:"candles"`
	}
	var apple struct {
		Market *struct {
			LongName         string `json:"long_name"`
			Exchange         string `json:"exchange"`
			InstrumentType   string `json:"instrument_type"`
			FiftyTwoWeekHigh string `json:"fifty_two_week_high"`
		} `json:"market"`
		Summary struct {
			FirstBoughtOn   string                `json:"first_bought_on"`
			InvestedCZK     struct{ Minor int64 } `json:"invested_czk"`
			SoldQuantity    string                `json:"sold_quantity"`
			RealizedGainCZK struct{ Minor int64 } `json:"realized_gain_czk"`
			RealizedCostCZK struct{ Minor int64 } `json:"realized_cost_czk"`
			DividendsNetCZK struct{ Minor int64 } `json:"dividends_net_czk"`
		} `json:"summary"`
	}
	c.do(http.MethodGet, "/api/v1/investments/instruments/US0378331005", nil, http.StatusOK, &apple)
	require.NotNil(t, apple.Market)
	assert.Equal(t, "Apple Inc.", apple.Market.LongName)
	assert.Equal(t, "NasdaqGS", apple.Market.Exchange)
	assert.Equal(t, "EQUITY", apple.Market.InstrumentType)
	assert.Equal(t, "348.15", apple.Market.FiftyTwoWeekHigh)
	assert.Equal(t, "2022-03-02", apple.Summary.FirstBoughtOn, "a sold lot tells of an earlier purchase")
	assert.Equal(t, int64(2*1797450), apple.Summary.InvestedCZK.Minor)
	assert.Equal(t, "3", apple.Summary.SoldQuantity)
	assert.Equal(t, int64(385912+29000), apple.Summary.RealizedGainCZK.Minor)
	assert.Equal(t, int64(744088+546000), apple.Summary.RealizedCostCZK.Minor)
	assert.Equal(t, int64(4876), apple.Summary.DividendsNetCZK.Minor)

	c.do(http.MethodGet, "/api/v1/investments/instruments/IE00B4L5Y983/candles?from=2026-01-01", nil, http.StatusOK, &candles)
	require.Len(t, candles.Candles, 1)
	assert.Equal(t, "2026-10-16", candles.Candles[0].Day)
	assert.Equal(t, "100", candles.Candles[0].Close)
	assert.Nil(t, candles.Candles[0].Open)
}

func TestRealizedGainsSplitByTheTimeTest(t *testing.T) {
	c := newMarketClient(t)
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, nil)

	var realized struct {
		Years []struct {
			Year               int                   `json:"year"`
			Lots               int                   `json:"lots"`
			ExemptGainCZK      struct{ Minor int64 } `json:"exempt_gain_czk"`
			TaxableGainCZK     struct{ Minor int64 } `json:"taxable_gain_czk"`
			TaxableProceedsCZK struct{ Minor int64 } `json:"taxable_proceeds_czk"`
			DividendsTaxCZK    struct{ Minor int64 } `json:"dividends_tax_czk"`
		} `json:"years"`
		Lots []struct {
			Exempt bool `json:"exempt"`
		} `json:"lots"`
	}
	c.do(http.MethodGet, "/api/v1/investments/realized", nil, http.StatusOK, &realized)
	require.Len(t, realized.Years, 1)
	y := realized.Years[0]
	assert.Equal(t, 2026, y.Year)
	assert.Equal(t, 3, y.Lots)
	assert.Equal(t, int64(385912), y.ExemptGainCZK.Minor)
	assert.Equal(t, int64(29000+3075), y.TaxableGainCZK.Minor)
	assert.Equal(t, int64(575000+33950), y.TaxableProceedsCZK.Minor)
	assert.Equal(t, int64(874), y.DividendsTaxCZK.Minor)
	require.Len(t, realized.Lots, 3)
}

func TestInvestmentImportDedupesAndRollsBack(t *testing.T) {
	c := newMarketClient(t)
	content := investmentStatement(t)
	var first, overlapping importResult
	c.upload("/api/v1/imports", "investments.csv", content, http.StatusCreated, &first)
	c.upload("/api/v1/imports", "investments-again.csv", append(content, '\n'), http.StatusCreated, &overlapping)
	assert.Zero(t, overlapping.RowsNew)
	assert.Equal(t, 8, overlapping.RowsDuplicate)

	var rollback struct {
		Removed int `json:"removed"`
	}
	c.do(http.MethodPost, "/api/v1/imports/"+first.BatchID+"/rollback", nil, http.StatusOK, &rollback)
	assert.Equal(t, 8, rollback.Removed)
	var p portfolio
	c.do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, &p)
	assert.Empty(t, p.Positions)
}

func TestInvestmentsAreOwnData(t *testing.T) {
	alice := newMarketClient(t)
	bob := alice.signUp("bob@coinbook.local")
	alice.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, nil)

	var p portfolio
	bob.do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, &p)
	assert.Empty(t, p.Positions)
	bob.do(http.MethodGet, "/api/v1/investments/instruments/US0378331005", nil, http.StatusNotFound, nil)
	bob.do(http.MethodGet, "/api/v1/investments/instruments/US0378331005/candles", nil, http.StatusNotFound, nil)
	bob.do(http.MethodPut, "/api/v1/investments/instruments/US0378331005/yahoo-symbol", map[string]string{"symbol": "MSFT"}, http.StatusNotFound, nil)
}

func TestYahooSymbolCanBeCorrected(t *testing.T) {
	c := newMarketClient(t)
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, nil)
	c.do(http.MethodPut, "/api/v1/investments/instruments/IE00B4L5Y983/yahoo-symbol", map[string]string{"symbol": "not a symbol"}, http.StatusUnprocessableEntity, nil)

	var detail struct {
		Instrument struct {
			YahooSymbol *string `json:"yahoo_symbol"`
		} `json:"instrument"`
		Trades []struct {
			Kind string `json:"kind"`
		} `json:"trades"`
		Sales []struct{} `json:"sales"`
	}
	c.do(http.MethodPut, "/api/v1/investments/instruments/IE00B4L5Y983/yahoo-symbol", map[string]string{"symbol": "eunl.de"}, http.StatusOK, &detail)
	assert.Equal(t, "EUNL.DE", *detail.Instrument.YahooSymbol)
	assert.Len(t, detail.Trades, 2)
	assert.Len(t, detail.Sales, 1)

	var cleared struct {
		Instrument struct {
			YahooSymbol *string `json:"yahoo_symbol"`
		} `json:"instrument"`
	}
	c.do(http.MethodPut, "/api/v1/investments/instruments/IE00B4L5Y983/yahoo-symbol", map[string]string{"symbol": ""}, http.StatusOK, &cleared)
	assert.Nil(t, cleared.Instrument.YahooSymbol)
}

func TestAgentTokenNeedsTheInvestmentsScope(t *testing.T) {
	c := newMarketClient(t)
	var created struct {
		Secret string `json:"secret"`
	}
	c.do(http.MethodPost, "/api/v1/agent-tokens", map[string]any{"name": "a", "scopes": []string{"reports:read"}}, http.StatusCreated, &created)
	c.withToken(created.Secret).do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusForbidden, nil)
	c.do(http.MethodPost, "/api/v1/agent-tokens", map[string]any{"name": "b", "scopes": []string{"investments:read"}}, http.StatusCreated, &created)
	c.withToken(created.Secret).do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, nil)
}

func TestYahooSymbolIsEachUsersOwn(t *testing.T) {
	alice := newMarketClient(t)
	bob := alice.signUp("bob@coinbook.local")
	alice.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, nil)
	bob.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, nil)

	alice.do(http.MethodPut, "/api/v1/investments/instruments/IE00B4L5Y983/yahoo-symbol", map[string]string{"symbol": "IWDA.AS"}, http.StatusOK, nil)

	var detail struct {
		Instrument struct {
			YahooSymbol *string `json:"yahoo_symbol"`
		} `json:"instrument"`
	}
	bob.do(http.MethodGet, "/api/v1/investments/instruments/IE00B4L5Y983", nil, http.StatusOK, &detail)
	assert.Equal(t, "EUNL.DE", *detail.Instrument.YahooSymbol, "Alice's choice is not Bob's")
}

func TestPricesAreFetchedOnlyForHeldSymbolsFromAFixedDay(t *testing.T) {
	var requested []string
	market := fakeMarket(t)
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cnb" {
			requested = append(requested, r.URL.Path+"?"+r.URL.Query().Get("period1"))
		}
		resp, err := http.Get(market.URL + r.URL.RequestURI())
		require.NoError(t, err)
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(recorder.Close)
	c := newServer(t, func(cfg *config.Config) {
		cfg.Market = config.MarketConfig{Enabled: true, YahooURL: recorder.URL, CNBURL: recorder.URL + "/cnb", UserAgent: "t", RefreshAtHour: 23}
	}).signUp("investor@coinbook.local")

	var imported importResult
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, &imported)
	assert.Zero(t, c.prices.Refresh(context.Background()))
	historyStart := strconv.FormatInt(time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), 10)
	assert.ElementsMatch(t, []string{"/v8/finance/chart/AAPL?" + historyStart, "/v8/finance/chart/EUNL.DE?" + historyStart}, requested)

	c.do(http.MethodPost, "/api/v1/imports/"+imported.BatchID+"/rollback", nil, http.StatusOK, nil)
	requested = nil
	assert.Zero(t, c.prices.Refresh(context.Background()))
	assert.Empty(t, requested, "nobody holds the instruments any more")
}

func TestAStatementReadByAnOlderParserCanBeImportedAgain(t *testing.T) {
	c := newMarketClient(t)
	var first importResult
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, &first)
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusConflict, nil)

	ctx := context.Background()
	var userID uuid.UUID
	require.NoError(t, pg.Pool.QueryRow(ctx, `SELECT id FROM app_user WHERE email = 'investor@coinbook.local'`).Scan(&userID))
	scoped, release, err := pg.Pool.ScopeToUser(ctx, userID)
	require.NoError(t, err)
	_, err = pg.Pool.Exec(scoped, `UPDATE import_batch SET parser_version = 1 WHERE id = $1`, first.BatchID)
	release()
	require.NoError(t, err)

	var again importResult
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, &again)
	assert.Zero(t, again.RowsNew)
	assert.Equal(t, 8, again.RowsDuplicate)
}

func TestAHostileQuantityIsRefused(t *testing.T) {
	c := newMarketClient(t)
	content := strings.Replace(string(investmentStatement(t)), ",1.5,Ireland,", ",1e-2147483000,Ireland,", 1)
	require.Contains(t, content, "1e-2147483000")
	c.upload("/api/v1/imports/preview", "investments.csv", []byte(content), http.StatusUnprocessableEntity, nil)
}

func TestASymbolYahooAnsweredEmptyIsAskedForItsWholeHistoryAgain(t *testing.T) {
	var requested []string
	market := fakeMarket(t)
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cnb" {
			requested = append(requested, r.URL.Path+"?"+r.URL.Query().Get("period1"))
		}
		resp, err := http.Get(market.URL + r.URL.RequestURI())
		require.NoError(t, err)
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(recorder.Close)
	c := newServer(t, func(cfg *config.Config) {
		cfg.Market = config.MarketConfig{Enabled: true, YahooURL: recorder.URL, CNBURL: recorder.URL + "/cnb", UserAgent: "t", RefreshAtHour: 23}
	}).signUp("investor@coinbook.local")
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, nil)
	c.do(http.MethodPut, "/api/v1/investments/instruments/IE00B4L5Y983/yahoo-symbol", map[string]string{"symbol": "NOPE.XX"}, http.StatusOK, nil)

	assert.Zero(t, c.prices.Refresh(context.Background()))
	requested = nil
	assert.Zero(t, c.prices.Refresh(context.Background()))
	historyStart := strconv.FormatInt(time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), 10)
	weekBack := strconv.FormatInt(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC).Unix(), 10)
	assert.Contains(t, requested, "/v8/finance/chart/NOPE.XX?"+historyStart, "an empty answer may be Yahoo's hiccup")
	assert.Contains(t, requested, "/v8/finance/chart/AAPL?"+weekBack, "a symbol with prices is asked for days")
}

func TestPositionsCanBeSplitPerAccount(t *testing.T) {
	c := newMarketClient(t)
	robo := "2. 2. 2026,EUNL,"
	appleInRobo := `2. 2. 2026,AAPL,US0378331005,1,United States,US$250,"5,250 CZK",US$0,0 CZK,US$0,0 CZK,US$0,0 CZK,US$250,"5,250 CZK",,,,,,,,` + "\n"
	content := strings.Replace(string(investmentStatement(t)), robo, strings.TrimSuffix(appleInRobo, "\n")+"\n"+robo, 1)
	require.Contains(t, content, appleInRobo)
	c.upload("/api/v1/imports", "investments.csv", []byte(content), http.StatusCreated, nil)
	require.Zero(t, c.prices.Refresh(context.Background()))

	type position struct {
		Instrument struct {
			Ticker string `json:"ticker"`
		} `json:"instrument"`
		AccountID *string `json:"account_id"`
		Quantity  string  `json:"quantity"`
	}
	var together, split struct {
		Positions []position `json:"positions"`
		ValueCZK  struct {
			Minor int64 `json:"minor"`
		} `json:"value_czk"`
	}
	c.do(http.MethodGet, "/api/v1/investments/portfolio", nil, http.StatusOK, &together)
	c.do(http.MethodGet, "/api/v1/investments/portfolio?per_account=true", nil, http.StatusOK, &split)

	require.Len(t, together.Positions, 2)
	assert.Equal(t, "6", together.Positions[0].Quantity)
	assert.Nil(t, together.Positions[0].AccountID)

	require.Len(t, split.Positions, 3)
	var apple []string
	for _, p := range split.Positions {
		require.NotNil(t, p.AccountID)
		if p.Instrument.Ticker == "AAPL" {
			apple = append(apple, p.Quantity)
		}
	}
	assert.ElementsMatch(t, []string{"5", "1"}, apple)
	assert.Equal(t, together.ValueCZK.Minor, split.ValueCZK.Minor)
}

func TestNetWorthAddsAccountsAndInvestmentsInCZK(t *testing.T) {
	c := newMarketClient(t)
	c.upload("/api/v1/imports", "mbank.csv", mbankStatement(t, ownMBankAccount, octoberRows), http.StatusCreated, nil)
	c.upload("/api/v1/imports", "investments.csv", investmentStatement(t), http.StatusCreated, nil)
	require.Zero(t, c.prices.Refresh(context.Background()))

	var accounts struct {
		Accounts []struct {
			Balance struct {
				Minor    int64  `json:"minor"`
				Currency string `json:"currency"`
			} `json:"balance"`
		} `json:"accounts"`
	}
	c.do(http.MethodGet, "/api/v1/accounts", nil, http.StatusOK, &accounts)
	var cash int64
	for _, a := range accounts.Accounts {
		if a.Balance.Currency == "CZK" {
			cash += a.Balance.Minor
		}
	}

	var worth struct {
		Total       struct{ Minor int64 } `json:"total_czk"`
		Cash        struct{ Minor int64 } `json:"cash_czk"`
		Investments struct{ Minor int64 } `json:"investments_czk"`
		Unconverted []string              `json:"unconverted"`
	}
	c.do(http.MethodGet, "/api/v1/reports/net-worth", nil, http.StatusOK, &worth)
	assert.Equal(t, cash, worth.Cash.Minor, "the broker accounts hold no cash of their own")
	assert.Equal(t, int64(3000000+344125), worth.Investments.Minor, "5 AAPL at 300 $ by 20 Kč, 1.37654322 EUNL at 100 € by 25 Kč")
	assert.Equal(t, worth.Cash.Minor+worth.Investments.Minor, worth.Total.Minor)
	assert.Empty(t, worth.Unconverted)
}
