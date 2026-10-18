package investment

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

var (
	ErrNotFound      = eris.New("investment: instrument not found")
	ErrInvalidSymbol = eris.New("investment: invalid Yahoo symbol")
)

type Instrument struct {
	ISIN   string
	Ticker string
	Name   string
	// Currency is the one the user's own trades are in; the shared register keeps none.
	Currency money.Currency
	// YahooSymbol is the user's own choice, else the guess from the first import; nil:
	// no prices, the position is valued at cost.
	YahooSymbol *string
}

type Dividend struct {
	AccountID     uuid.UUID
	ISIN          string
	PaidOn        time.Time
	Currency      money.Currency
	GrossMinor    int64
	NetMinor      int64
	GrossCZKMinor *int64
	NetCZKMinor   *int64
	TaxCZKMinor   int64
}

type Quote struct {
	Day      time.Time
	Close    decimal.Decimal
	Currency money.Currency
}

type Repo struct {
	pool *db.Pool
}

func NewRepo(pool *db.Pool) *Repo { return &Repo{pool: pool} }

// history is everything the signed-in user holds or held; isin narrows it to one instrument.
type history struct {
	instruments map[string]Instrument
	buys        []Buy
	sales       []LotSale
	dividends   []Dividend
}

func (r *Repo) history(ctx context.Context, isin *string) (*history, error) {
	h := &history{instruments: map[string]Instrument{}}
	rows, err := r.pool.Query(ctx, `
		SELECT i.isin, coalesce(i.ticker, ''), i.name,
			CASE WHEN own.isin IS NULL THEN i.yahoo_symbol ELSE own.yahoo_symbol END
		FROM instrument i LEFT JOIN instrument_symbol own ON own.isin = i.isin
		WHERE ($1::text IS NULL OR i.isin = $1) AND `+heldBySignedInUser, isin)
	if err != nil {
		return nil, eris.Wrap(err, "list instruments")
	}
	instruments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Instrument, error) {
		var i Instrument
		err := row.Scan(&i.ISIN, &i.Ticker, &i.Name, &i.YahooSymbol)
		return i, err
	})
	if err != nil {
		return nil, eris.Wrap(err, "read instruments")
	}
	for _, i := range instruments {
		h.instruments[i.ISIN] = i
	}

	rows, err = r.pool.Query(ctx, `
		SELECT account_id, isin, kind, traded_on, coalesce(quantity::text, '0'), currency, gross_minor, net_minor,
			gross_czk_minor, net_czk_minor, tax_czk_minor
		FROM investment_tx WHERE ($1::text IS NULL OR isin = $1) ORDER BY traded_on, source_line`, isin)
	if err != nil {
		return nil, eris.Wrap(err, "list investment transactions")
	}
	defer rows.Close()
	for rows.Next() {
		var d Dividend
		var kind, quantity string
		if err := rows.Scan(&d.AccountID, &d.ISIN, &kind, &d.PaidOn, &quantity, &d.Currency, &d.GrossMinor, &d.NetMinor,
			&d.GrossCZKMinor, &d.NetCZKMinor, &d.TaxCZKMinor); err != nil {
			return nil, eris.Wrap(err, "read investment transaction")
		}
		if kind == "dividend" {
			h.dividends = append(h.dividends, d)
			continue
		}
		q, err := decimal.NewFromString(quantity)
		if err != nil {
			return nil, eris.Wrap(err, "read quantity")
		}
		h.buys = append(h.buys, Buy{AccountID: d.AccountID, ISIN: d.ISIN, TradedOn: d.PaidOn, Quantity: q,
			Currency: d.Currency, NetMinor: d.NetMinor, NetCZKMinor: d.NetCZKMinor})
	}
	if err := rows.Err(); err != nil {
		return nil, eris.Wrap(err, "read investment transactions")
	}

	rows, err = r.pool.Query(ctx, `
		SELECT account_id, isin, bought_on, sold_on, quantity::text, currency, proceeds_minor, cost_minor, gain_minor,
			proceeds_czk_minor, cost_czk_minor, gain_czk_minor
		FROM investment_lot_sale WHERE ($1::text IS NULL OR isin = $1) ORDER BY sold_on, source_line`, isin)
	if err != nil {
		return nil, eris.Wrap(err, "list sold lots")
	}
	h.sales, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (LotSale, error) {
		var s LotSale
		var quantity string
		if err := row.Scan(&s.AccountID, &s.ISIN, &s.BoughtOn, &s.SoldOn, &quantity, &s.Currency, &s.ProceedsMinor,
			&s.CostMinor, &s.GainMinor, &s.ProceedsCZKMinor, &s.CostCZKMinor, &s.GainCZKMinor); err != nil {
			return s, err
		}
		s.Quantity, err = decimal.NewFromString(quantity)
		return s, err
	})
	if err != nil {
		return nil, eris.Wrap(err, "read sold lots")
	}
	h.setCurrencies()
	return h, nil
}

// heldBySignedInUser limits instruments to those in the user's own records (row security
// filters both tables), so the shared register reveals nothing another user holds.
const heldBySignedInUser = `(EXISTS (SELECT 1 FROM investment_tx t WHERE t.isin = i.isin)
	OR EXISTS (SELECT 1 FROM investment_lot_sale s WHERE s.isin = i.isin))`

// setCurrencies gives each instrument the currency of the user's earliest record of it.
func (h *history) setCurrencies() {
	set := func(isin string, currency money.Currency) {
		if i, ok := h.instruments[isin]; ok && i.Currency == "" {
			i.Currency = currency
			h.instruments[isin] = i
		}
	}
	for _, s := range h.sales {
		set(s.ISIN, s.Currency)
	}
	for _, b := range h.buys {
		set(b.ISIN, b.Currency)
	}
	for _, d := range h.dividends {
		set(d.ISIN, d.Currency)
	}
}

// latestQuotes is the last close of each symbol.
func (r *Repo) latestQuotes(ctx context.Context, symbols []string) (map[string]Quote, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (b.symbol) b.symbol, b.day, b.close::text, s.currency
		FROM price_bar b JOIN price_series s ON s.symbol = b.symbol
		WHERE b.symbol = ANY($1) AND s.currency IS NOT NULL ORDER BY b.symbol, b.day DESC`, symbols)
	if err != nil {
		return nil, eris.Wrap(err, "latest prices")
	}
	quotes := map[string]Quote{}
	defer rows.Close()
	for rows.Next() {
		var symbol, closePrice string
		var q Quote
		if err := rows.Scan(&symbol, &q.Day, &closePrice, &q.Currency); err != nil {
			return nil, eris.Wrap(err, "read latest price")
		}
		if q.Close, err = decimal.NewFromString(closePrice); err != nil {
			return nil, eris.Wrap(err, "read latest price")
		}
		quotes[symbol] = q
	}
	return quotes, eris.Wrap(rows.Err(), "read latest prices")
}

type Rate struct {
	Day        time.Time
	CZKPerUnit decimal.Decimal
}

func (r *Repo) latestRates(ctx context.Context) (map[money.Currency]Rate, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT ON (currency) currency, day, czk_per_unit::text FROM fx_rate ORDER BY currency, day DESC`)
	if err != nil {
		return nil, eris.Wrap(err, "latest rates")
	}
	rates := map[money.Currency]Rate{}
	defer rows.Close()
	for rows.Next() {
		var currency, value string
		var rate Rate
		if err := rows.Scan(&currency, &rate.Day, &value); err != nil {
			return nil, eris.Wrap(err, "read rate")
		}
		if rate.CZKPerUnit, err = decimal.NewFromString(value); err != nil {
			return nil, eris.Wrap(err, "read rate")
		}
		rates[money.Currency(currency)] = rate
	}
	return rates, eris.Wrap(rows.Err(), "read rates")
}

type Candle struct {
	Day    time.Time
	Open   decimal.NullDecimal
	High   decimal.NullDecimal
	Low    decimal.NullDecimal
	Close  decimal.Decimal
	Volume *int64
}

// seriesCurrency is nil until the symbol's prices are fetched.
func (r *Repo) seriesCurrency(ctx context.Context, symbol string) (*money.Currency, error) {
	var currency *money.Currency
	err := r.pool.QueryRow(ctx, `SELECT (SELECT currency FROM price_series WHERE symbol = $1)`, symbol).Scan(&currency)
	if currency != nil {
		trimmed := money.Currency(strings.TrimSpace(string(*currency)))
		currency = &trimmed
	}
	return currency, eris.Wrap(err, "read quote currency")
}

func (r *Repo) candles(ctx context.Context, symbol string, from time.Time) ([]Candle, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT day, open::text, high::text, low::text, close::text, volume FROM price_bar
		WHERE symbol = $1 AND day >= $2 ORDER BY day`, symbol, from)
	if err != nil {
		return nil, eris.Wrap(err, "list prices")
	}
	candles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candle, error) {
		var c Candle
		var open, high, low *string
		var closePrice string
		if err := row.Scan(&c.Day, &open, &high, &low, &closePrice, &c.Volume); err != nil {
			return c, err
		}
		var err error
		if c.Close, err = decimal.NewFromString(closePrice); err != nil {
			return c, err
		}
		for _, v := range []struct {
			raw *string
			to  *decimal.NullDecimal
		}{{open, &c.Open}, {high, &c.High}, {low, &c.Low}} {
			if v.raw != nil {
				if v.to.Decimal, err = decimal.NewFromString(*v.raw); err != nil {
					return c, err
				}
				v.to.Valid = true
			}
		}
		return c, nil
	})
	return candles, eris.Wrap(err, "read prices")
}

// SetYahooSymbol records the user's own symbol for an instrument they hold; nil prices
// it at cost. Other holders keep theirs.
func (r *Repo) SetYahooSymbol(ctx context.Context, isin string, symbol *string) error {
	var held bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM instrument i WHERE i.isin = $1 AND `+heldBySignedInUser+`)`,
		isin).Scan(&held); err != nil {
		return eris.Wrap(err, "check holding")
	}
	if !held {
		return ErrNotFound
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO instrument_symbol (isin, yahoo_symbol) VALUES ($1, $2)
		ON CONFLICT (user_id, isin) DO UPDATE SET yahoo_symbol = excluded.yahoo_symbol`, isin, symbol)
	return eris.Wrap(err, "set Yahoo symbol")
}

// maxHeldSymbols bounds what one user's imports make the server ask Yahoo for on each run;
// a household portfolio is far below it, a statement of invented instruments is not.
const maxHeldSymbols = 300

// HeldSymbols are the Yahoo symbols the signed-in user prices their holdings by.
func (r *Repo) HeldSymbols(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT symbol FROM (
			SELECT CASE WHEN own.isin IS NULL THEN i.yahoo_symbol ELSE own.yahoo_symbol END AS symbol
			FROM instrument i LEFT JOIN instrument_symbol own ON own.isin = i.isin
			WHERE `+heldBySignedInUser+`) held
		WHERE symbol IS NOT NULL ORDER BY symbol LIMIT $1`, maxHeldSymbols)
	if err != nil {
		return nil, eris.Wrap(err, "list held symbols")
	}
	symbols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return symbols, eris.Wrap(err, "read held symbols")
}
