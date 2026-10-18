package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const chartBody = `{"chart":{"result":[{"meta":{"currency":"USD","gmtoffset":-14400,"longName":"Apple Inc.",
	"fullExchangeName":"NasdaqGS","instrumentType":"EQUITY","exchangeTimezoneName":"America/New_York",
	"firstTradeDate":345479400,"fiftyTwoWeekLow":243.42,"fiftyTwoWeekHigh":348.15,"regularMarketPrice":343.4599914550781,
	"regularMarketChangePercent":0.42700000001,"regularMarketVolume":59852690,"regularMarketTime":1792180801},
	"timestamp":[1767969000,1768055400,1768314600],
	"indicators":{"quote":[{"open":[100.5,null,102],"high":[101,null,103.25],"low":[99,null,101],
	"close":[100.75,null,102.5],"volume":[1000000,null,2500000]}]}}],"error":null}}`

func TestYahooDailyBars(t *testing.T) {
	var gotPath, gotAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAgent = r.URL.Path+"?"+r.URL.RawQuery, r.UserAgent()
		_, _ = w.Write([]byte(chartBody))
	}))
	defer server.Close()

	from := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
	chart, err := NewYahoo(server.URL, "coin-book-test").DailyBars(context.Background(), "eunl.de", from, from.AddDate(0, 0, 4))
	require.NoError(t, err)

	assert.Equal(t, "/v8/finance/chart/EUNL.DE?interval=1d&period1=1767916800&period2=1768348800", gotPath)
	assert.Equal(t, "coin-book-test", gotAgent)
	assert.Equal(t, "USD", chart.Currency)
	require.Len(t, chart.Bars, 2, "a day without a close is skipped")
	assert.Equal(t, time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC), chart.Bars[0].Day)
	assert.Equal(t, "100.75", chart.Bars[0].Close.String())
	assert.Equal(t, "100.5", chart.Bars[0].Open.Decimal.String())
	assert.Equal(t, int64(1000000), *chart.Bars[0].Volume)
	assert.Equal(t, time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC), chart.Bars[1].Day)

	p := chart.Profile
	require.NotNil(t, p)
	assert.Equal(t, "Apple Inc.", p.LongName)
	assert.Equal(t, "NasdaqGS", p.Exchange)
	assert.Equal(t, "EQUITY", p.InstrumentType)
	assert.Equal(t, time.Date(1980, 12, 12, 0, 0, 0, 0, time.UTC), *p.FirstTradeOn)
	assert.Equal(t, "343.46", p.MarketPrice.Decimal.String())
	assert.Equal(t, "0.427", p.MarketChangePercent.Decimal.String())
	assert.Equal(t, int64(59852690), *p.DayVolume)
	assert.False(t, p.DayLow.Valid, "a field Yahoo leaves out stays empty")
}

func TestYahooUnknownSymbolIsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	chart, err := NewYahoo(server.URL, "t").DailyBars(context.Background(), "NOPE", time.Now(), time.Now())
	require.NoError(t, err)
	assert.Empty(t, chart.Bars)
}

func TestYahooRetriesOnceAfterRateLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(chartBody))
	}))
	defer server.Close()
	y := NewYahoo(server.URL, "t")
	chart, err := y.DailyBars(context.Background(), "AAPL", time.Now(), time.Now())
	require.NoError(t, err)
	assert.Len(t, chart.Bars, 2)
	assert.Equal(t, int32(2), calls.Load())
}

func TestYahooGivesUpWhenStillRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	_, err := NewYahoo(server.URL, "t").DailyBars(context.Background(), "AAPL", time.Now(), time.Now())
	assert.ErrorIs(t, err, ErrRateLimited)
}

func TestYahooRefusesOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxResponseBytes+1))
	}))
	defer server.Close()
	_, err := NewYahoo(server.URL, "t").DailyBars(context.Background(), "AAPL", time.Now(), time.Now())
	assert.Error(t, err)
}

func TestParseCNB(t *testing.T) {
	rates, err := parseCNB([]byte("17.10.2026 #201\nzemě|měna|množství|kód|kurz\n" +
		"EMU|euro|1|EUR|24,330\nJaponsko|jen|100|JPY|15,512\nUSA|dolar|1|USD|21,045\n"))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), rates.Day)
	assert.Equal(t, "24.33", rates.CZKPerUnit["EUR"].String())
	assert.Equal(t, "0.15512", rates.CZKPerUnit["JPY"].String(), "per one unit, not per hundred")
}

func TestParseCNBRejectsGarbage(t *testing.T) {
	_, err := parseCNB([]byte("<html>maintenance</html>"))
	assert.Error(t, err)
}
