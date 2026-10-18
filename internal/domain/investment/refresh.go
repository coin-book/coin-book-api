package investment

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/marketdata"
)

type PriceSource interface {
	DailyBars(ctx context.Context, symbol string, from, to time.Time) (*marketdata.Chart, error)
}

type RateSource interface {
	Latest(ctx context.Context) (*marketdata.Rates, error)
}

// refetchDays overlaps each fetch with the last one: Yahoo revises recent days, and
// today's bar changes until the close.
const refetchDays = 7

// historyStart is where every new symbol's prices begin. A fixed day, not one relative to
// the first fetch, so the shared prices reveal neither since when nor since when first
// anyone holds an instrument.
var historyStart = time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC)

// manualRefreshCooldown keeps repeated clicks from hammering Yahoo from one address.
const manualRefreshCooldown = time.Minute

// Refresher keeps the shared price and rate tables current: once at start, daily at
// a set hour, and whenever triggered. It works outside any user's scope and touches
// only market data.
type Refresher struct {
	pool    *db.Pool
	repo    *Repo
	prices  PriceSource
	rates   RateSource
	clock   clock.Clock
	atHour  int
	trigger chan struct{}

	mu     sync.Mutex
	status RefreshStatus
}

type RefreshStatus struct {
	Running bool
	// FinishedAt is nil until the first refresh ends.
	FinishedAt *time.Time
	Failures   int
}

// NewRefresher takes nil sources when market data is disabled; it then never fetches.
func NewRefresher(pool *db.Pool, prices PriceSource, rates RateSource, c clock.Clock, atHour int) *Refresher {
	return &Refresher{pool: pool, repo: NewRepo(pool), prices: prices, rates: rates, clock: c, atHour: atHour,
		trigger: make(chan struct{}, 1)}
}

func (r *Refresher) Enabled() bool { return r.prices != nil && r.rates != nil }

// Trigger asks for a refresh soon; requests while one is pending collapse into it.
func (r *Refresher) Trigger() {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queueLocked()
}

// RequestRefresh is the user's button: dropped within a minute of the last refresh.
func (r *Refresher) RequestRefresh() {
	if !r.Enabled() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if finished := r.status.FinishedAt; !r.status.Running && finished != nil && r.clock.Now().Sub(*finished) < manualRefreshCooldown {
		return
	}
	r.queueLocked()
}

// queueLocked runs with mu held.
func (r *Refresher) queueLocked() {
	r.status.Running = true
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *Refresher) Status() RefreshStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Refresher) Run(ctx context.Context) {
	if !r.Enabled() {
		return
	}
	for {
		r.refreshLogged(ctx)
		timer := time.NewTimer(nextRefresh(r.clock.Now(), r.atHour).Sub(r.clock.Now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-r.trigger:
			timer.Stop()
		}
	}
}

func nextRefresh(now time.Time, atHour int) time.Time {
	local := now.In(clock.BusinessZone)
	next := time.Date(local.Year(), local.Month(), local.Day(), atHour, 0, 0, 0, clock.BusinessZone)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (r *Refresher) refreshLogged(ctx context.Context) {
	r.mu.Lock()
	r.status.Running = true
	r.mu.Unlock()
	failures := r.Refresh(ctx)
	finished := r.clock.Now()
	r.mu.Lock()
	// A trigger that arrived during this run is still in the channel and runs next.
	r.status = RefreshStatus{FinishedAt: &finished, Failures: failures, Running: len(r.trigger) > 0}
	r.mu.Unlock()
}

type refreshTarget struct {
	symbol string
	from   time.Time
}

// Refresh fetches rates and every instrument's prices; one failing source or symbol
// does not stop the others. It returns how many failed.
func (r *Refresher) Refresh(ctx context.Context) int {
	failures := 0
	if err := r.refreshRates(ctx); err != nil {
		slog.WarnContext(ctx, "exchange rates not refreshed", "err", err)
		failures++
	}
	today := clock.Today(r.clock)
	targets, err := r.targets(ctx, today)
	if err != nil {
		slog.ErrorContext(ctx, "prices not refreshed", "err", err)
		return failures + 1
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			return failures
		}
		if err := r.refreshInstrument(ctx, t, today); err != nil {
			slog.WarnContext(ctx, "prices not refreshed", "symbol", t.symbol, "err", err)
			failures++
		}
	}
	return failures
}

func (r *Refresher) refreshRates(ctx context.Context) error {
	rates, err := r.rates.Latest(ctx)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for currency, rate := range rates.CZKPerUnit {
		batch.Queue(`INSERT INTO fx_rate (day, currency, czk_per_unit, source) VALUES ($1, $2, $3::numeric, 'cnb')
			ON CONFLICT (currency, day) DO UPDATE SET czk_per_unit = excluded.czk_per_unit`,
			rates.Day, currency, rate.String())
	}
	return eris.Wrap(r.sendBatch(ctx, batch), "store exchange rates")
}

// targets are the symbols some user prices a holding by. Holdings are each user's own
// rows, so they are read in each user's scope; only the symbols leave it.
func (r *Refresher) targets(ctx context.Context, today time.Time) ([]refreshTarget, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM app_user ORDER BY id`)
	if err != nil {
		return nil, eris.Wrap(err, "list users")
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, eris.Wrap(err, "read users")
	}
	held := map[string]bool{}
	for _, user := range users {
		symbols, err := r.heldSymbols(ctx, user)
		if err != nil {
			return nil, err
		}
		if len(symbols) == maxHeldSymbols {
			slog.WarnContext(ctx, "held symbols capped; the rest get no prices", "user", user, "cap", maxHeldSymbols)
		}
		for _, s := range symbols {
			held[s] = true
		}
	}
	symbols := slices.Sorted(maps.Keys(held))
	// Until a symbol has had prices, it is asked for from the start: Yahoo also answers a
	// valid symbol empty now and then.
	rows, err = r.pool.Query(ctx, `SELECT symbol, fetched_through FROM price_series
		WHERE symbol = ANY($1) AND currency IS NOT NULL`, symbols)
	if err != nil {
		return nil, eris.Wrap(err, "read price series")
	}
	fetched := map[string]time.Time{}
	defer rows.Close()
	for rows.Next() {
		var symbol string
		var through time.Time
		if err := rows.Scan(&symbol, &through); err != nil {
			return nil, eris.Wrap(err, "read price series")
		}
		fetched[symbol] = through
	}
	if err := rows.Err(); err != nil {
		return nil, eris.Wrap(err, "read price series")
	}
	targets := make([]refreshTarget, 0, len(symbols))
	for _, symbol := range symbols {
		from := historyStart
		if through, ok := fetched[symbol]; ok {
			from = through.AddDate(0, 0, -refetchDays)
		}
		targets = append(targets, refreshTarget{symbol: symbol, from: from})
	}
	return targets, nil
}

func (r *Refresher) heldSymbols(ctx context.Context, user uuid.UUID) ([]string, error) {
	scoped, release, err := r.pool.ScopeToUser(ctx, user)
	if err != nil {
		return nil, err
	}
	defer release()
	return r.repo.HeldSymbols(scoped)
}

func (r *Refresher) refreshInstrument(ctx context.Context, t refreshTarget, today time.Time) error {
	chart, err := r.prices.DailyBars(ctx, t.symbol, t.from, today)
	if err != nil {
		return err
	}
	if len(chart.Bars) > 0 && len(chart.Currency) != 3 {
		return eris.Errorf("quote currency %q", chart.Currency)
	}
	// A symbol Yahoo answers empty is recorded too; once it has a currency, later runs ask for days, not years.
	var currency *string
	if len(chart.Currency) == 3 {
		currency = &chart.Currency
	}
	batch := &pgx.Batch{}
	for _, b := range chart.Bars {
		batch.Queue(`
			INSERT INTO price_bar (symbol, day, open, high, low, close, volume, source)
			VALUES ($1, $2, $3::numeric, $4::numeric, $5::numeric, $6::numeric, $7, 'yahoo')
			ON CONFLICT (symbol, day) DO UPDATE SET open = excluded.open, high = excluded.high, low = excluded.low,
				close = excluded.close, volume = excluded.volume, source = excluded.source`,
			t.symbol, b.Day, nullText(b.Open), nullText(b.High), nullText(b.Low), b.Close.String(), b.Volume)
	}
	batch.Queue(`
		INSERT INTO price_series (symbol, currency, fetched_through) VALUES ($1, $2, $3)
		ON CONFLICT (symbol) DO UPDATE SET currency = coalesce(excluded.currency, price_series.currency),
			fetched_through = excluded.fetched_through`,
		t.symbol, currency, today)
	if p := chart.Profile; p != nil && currency != nil {
		batch.Queue(`
			UPDATE price_series SET long_name = nullif($2, ''), exchange = nullif($3, ''), instrument_type = nullif($4, ''),
				exchange_timezone = nullif($5, ''), first_trade_on = $6, fifty_two_week_low = $7::numeric,
				fifty_two_week_high = $8::numeric, day_low = $9::numeric, day_high = $10::numeric, day_volume = $11,
				market_price = $12::numeric, market_change_percent = $13::numeric, market_time = $14
			WHERE symbol = $1`,
			t.symbol, p.LongName, p.Exchange, p.InstrumentType, p.ExchangeTimezone, p.FirstTradeOn,
			nullText(p.FiftyTwoWeekLow), nullText(p.FiftyTwoWeekHigh), nullText(p.DayLow), nullText(p.DayHigh), p.DayVolume,
			nullText(p.MarketPrice), nullText(p.MarketChangePercent), p.MarketTime)
	}
	return eris.Wrap(r.sendBatch(ctx, batch), "store prices")
}

func (r *Refresher) sendBatch(ctx context.Context, batch *pgx.Batch) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error { return tx.SendBatch(ctx, batch).Close() })
}

func nullText(d decimal.NullDecimal) *string {
	if !d.Valid {
		return nil
	}
	s := d.Decimal.String()
	return &s
}
