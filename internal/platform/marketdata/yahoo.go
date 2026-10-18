package marketdata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"
)

// Bar is one trading day. Open, high and low may be missing where Yahoo has only a close.
type Bar struct {
	Day    time.Time
	Open   decimal.NullDecimal
	High   decimal.NullDecimal
	Low    decimal.NullDecimal
	Close  decimal.Decimal
	Volume *int64
}

// Chart is a symbol's daily bars in the currency the exchange quotes it in.
type Chart struct {
	Currency string
	Bars     []Bar
}

// Yahoo reads the public chart endpoint, the same one the trading project uses
// (trading3 internal/domain/ingest/prices/yahoo.go); it needs no crumb or cookie.
// Closes are split-adjusted.
type Yahoo struct {
	client  *client
	baseURL string
}

func NewYahoo(baseURL, userAgent string) *Yahoo {
	return &Yahoo{client: newClient(userAgent), baseURL: strings.TrimRight(baseURL, "/")}
}

type yahooChart struct {
	Chart struct {
		Result []struct {
			Meta struct {
				Currency  string `json:"currency"`
				GMTOffset int64  `json:"gmtoffset"`
			} `json:"meta"`
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []*float64 `json:"open"`
					High   []*float64 `json:"high"`
					Low    []*float64 `json:"low"`
					Close  []*float64 `json:"close"`
					Volume []*float64 `json:"volume"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// DailyBars fetches the days from..to inclusive. An unknown symbol is an empty chart.
func (y *Yahoo) DailyBars(ctx context.Context, symbol string, from, to time.Time) (*Chart, error) {
	q := url.Values{}
	q.Set("period1", strconv.FormatInt(from.UTC().Unix(), 10))
	// period2 is exclusive.
	q.Set("period2", strconv.FormatInt(to.UTC().AddDate(0, 0, 1).Unix(), 10))
	q.Set("interval", "1d")
	endpoint := y.baseURL + "/v8/finance/chart/" + url.PathEscape(strings.ToUpper(symbol)) + "?" + q.Encode()

	res, err := y.client.get(ctx, endpoint)
	if err != nil {
		return nil, eris.Wrapf(err, "yahoo %s", symbol)
	}
	if res.status == http.StatusNotFound {
		return &Chart{}, nil
	}
	if res.status != http.StatusOK {
		return nil, eris.Errorf("yahoo %s: status %d", symbol, res.status)
	}
	return parseYahooChart(res.body, symbol)
}

func parseYahooChart(body []byte, symbol string) (*Chart, error) {
	var parsed yahooChart
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, eris.Wrapf(err, "yahoo %s: decode", symbol)
	}
	if parsed.Chart.Error != nil {
		return nil, eris.Errorf("yahoo %s: %s", symbol, parsed.Chart.Error.Description)
	}
	if len(parsed.Chart.Result) == 0 {
		return &Chart{}, nil
	}
	result := parsed.Chart.Result[0]
	chart := &Chart{Currency: result.Meta.Currency}
	if len(result.Indicators.Quote) == 0 {
		return chart, nil
	}
	quote := result.Indicators.Quote[0]
	for i, ts := range result.Timestamp {
		// Yahoo pads days without data with nulls; a bar without a close is no bar.
		closePrice, ok := at(quote.Close, i)
		if !ok {
			continue
		}
		// Timestamps are the session open; the exchange's offset gives its trading day.
		local := time.Unix(ts+result.Meta.GMTOffset, 0).UTC()
		bar := Bar{
			Day:   time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC),
			Close: price(closePrice),
			Open:  nullDecimal(quote.Open, i),
			High:  nullDecimal(quote.High, i),
			Low:   nullDecimal(quote.Low, i),
		}
		if v, ok := at(quote.Volume, i); ok {
			volume := int64(v)
			bar.Volume = &volume
		}
		chart.Bars = append(chart.Bars, bar)
	}
	return chart, nil
}

func at(values []*float64, i int) (float64, bool) {
	if i >= len(values) || values[i] == nil {
		return 0, false
	}
	return *values[i], true
}

func nullDecimal(values []*float64, i int) decimal.NullDecimal {
	v, ok := at(values, i)
	if !ok {
		return decimal.NullDecimal{}
	}
	return decimal.NullDecimal{Decimal: price(v), Valid: true}
}

// priceDecimals drops the float noise Yahoo's single-precision values carry (343.45999146).
const priceDecimals = 4

func price(v float64) decimal.Decimal { return decimal.NewFromFloat(v).Round(priceDecimals) }
