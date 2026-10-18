package investment

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/money"
)

type Service struct {
	repo  *Repo
	clock clock.Clock
}

func NewService(repo *Repo, c clock.Clock) *Service { return &Service{repo: repo, clock: c} }

type Position struct {
	Instrument Instrument
	Quantity   decimal.Decimal
	Lots       []Lot
	// CostMinor is in CostCurrency, the lots' currency; zero when MixedCurrencies.
	CostMinor       int64
	CostCurrency    money.Currency
	MixedCurrencies bool
	// CostCZKMinor is nil when a lot has no CZK cost.
	CostCZKMinor *int64
	// Quote is nil without a price; the position is then valued at cost.
	Quote      *Quote
	ValueMinor *int64
	// ValueCZKMinor is nil without a price or without an exchange rate.
	ValueCZKMinor        *int64
	DividendsNetCZKMinor int64
	// IncompleteHistory: some sold lot has no purchase in the imported statements.
	IncompleteHistory bool
	// ExemptQuantity is held long enough to pass the time test today.
	ExemptQuantity decimal.Decimal
	// NextExemptOn is when the next lot passes the time test; nil when none is waiting.
	NextExemptOn *time.Time
}

type Portfolio struct {
	Positions []Position
	// ValueCZKMinor and CostCZKMinor sum the positions that have both, so they compare.
	ValueCZKMinor int64
	CostCZKMinor  int64
	// Unvalued counts positions left out of the totals: no price, rate or CZK cost.
	Unvalued int
	// RatesOn is the day of the ČNB rates used; nil when there are none yet.
	RatesOn *time.Time
	// IncompleteHistory lists instruments with sold lots whose purchase was never imported:
	// shares bought before the first imported statement are missing from the positions.
	IncompleteHistory []Instrument
}

func (s *Service) Portfolio(ctx context.Context) (*Portfolio, error) {
	h, err := s.repo.history(ctx, nil)
	if err != nil {
		return nil, err
	}
	positions, err := s.positions(ctx, h)
	if err != nil {
		return nil, err
	}
	p := &Portfolio{Positions: positions, IncompleteHistory: []Instrument{}}
	_, incomplete := OpenLots(h.buys, h.sales)
	for isin := range incomplete {
		p.IncompleteHistory = append(p.IncompleteHistory, h.instruments[isin])
	}
	slices.SortFunc(p.IncompleteHistory, func(a, b Instrument) int { return strings.Compare(a.label(), b.label()) })
	for _, pos := range positions {
		if pos.ValueCZKMinor == nil || pos.CostCZKMinor == nil {
			p.Unvalued++
			continue
		}
		p.ValueCZKMinor += *pos.ValueCZKMinor
		p.CostCZKMinor += *pos.CostCZKMinor
	}
	rates, err := s.repo.latestRates(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rates {
		if p.RatesOn == nil || r.Day.After(*p.RatesOn) {
			day := r.Day
			p.RatesOn = &day
		}
	}
	return p, nil
}

func (s *Service) positions(ctx context.Context, h *history) ([]Position, error) {
	lots, incomplete := OpenLots(h.buys, h.sales)
	byISIN := map[string]*Position{}
	var order []string
	for _, lot := range lots {
		pos, ok := byISIN[lot.ISIN]
		if !ok {
			pos = &Position{Instrument: h.instruments[lot.ISIN], CostCZKMinor: new(int64)}
			byISIN[lot.ISIN] = pos
			order = append(order, lot.ISIN)
		}
		pos.Lots = append(pos.Lots, lot)
		pos.Quantity = pos.Quantity.Add(lot.Quantity)
		if pos.CostCurrency == "" {
			pos.CostCurrency = lot.Currency
		}
		if lot.Currency != pos.CostCurrency {
			pos.MixedCurrencies = true
		}
		pos.CostMinor += lot.CostMinor
		if lot.CostCZKMinor == nil || pos.CostCZKMinor == nil {
			pos.CostCZKMinor = nil
		} else {
			*pos.CostCZKMinor += *lot.CostCZKMinor
		}
	}
	for _, d := range h.dividends {
		if pos, ok := byISIN[d.ISIN]; ok && d.NetCZKMinor != nil {
			pos.DividendsNetCZKMinor += *d.NetCZKMinor
		}
	}

	var symbols []string
	for _, pos := range byISIN {
		if pos.Instrument.YahooSymbol != nil {
			symbols = append(symbols, *pos.Instrument.YahooSymbol)
		}
	}
	quotes, err := s.repo.latestQuotes(ctx, symbols)
	if err != nil {
		return nil, err
	}
	rates, err := s.repo.latestRates(ctx)
	if err != nil {
		return nil, err
	}
	today := clock.Today(s.clock)
	positions := make([]Position, 0, len(order))
	for _, isin := range order {
		pos := byISIN[isin]
		if pos.MixedCurrencies {
			pos.CostMinor = 0
		}
		pos.IncompleteHistory = incomplete[isin]
		pos.timeTest(today)
		if pos.Instrument.YahooSymbol != nil {
			if q, ok := quotes[*pos.Instrument.YahooSymbol]; ok {
				pos.value(q, rates)
			}
		}
		positions = append(positions, *pos)
	}
	slices.SortFunc(positions, func(a, b Position) int {
		return strings.Compare(a.Instrument.label(), b.Instrument.label())
	})
	return positions, nil
}

func (i Instrument) label() string {
	if i.Ticker != "" {
		return i.Ticker
	}
	return i.Name
}

func (p *Position) timeTest(today time.Time) {
	for _, lot := range p.Lots {
		exemptFrom := ExemptFrom(lot.BoughtOn)
		if !today.Before(exemptFrom) {
			p.ExemptQuantity = p.ExemptQuantity.Add(lot.Quantity)
			continue
		}
		if p.NextExemptOn == nil || exemptFrom.Before(*p.NextExemptOn) {
			p.NextExemptOn = &exemptFrom
		}
	}
}

// value prices the position in the quote's currency, which need not be the trades'. Yahoo
// quotes London listings in pence ("GBp"), which is no ISO code: such a quote values nothing.
func (p *Position) value(q Quote, rates map[money.Currency]Rate) {
	if !q.Currency.Valid() {
		return
	}
	p.Quote = &q
	value := p.Quantity.Mul(q.Close).Mul(minorPerUnit(q.Currency)).Round(0).IntPart()
	p.ValueMinor = &value
	if inCZK, ok := toCZK(value, q.Currency, rates); ok {
		p.ValueCZKMinor = &inCZK
	}
}

func toCZK(minor int64, currency money.Currency, rates map[money.Currency]Rate) (int64, bool) {
	if currency == money.CZK {
		return minor, true
	}
	rate, ok := rates[currency]
	if !ok {
		return 0, false
	}
	units := decimal.NewFromInt(minor).Div(minorPerUnit(currency))
	return units.Mul(rate.CZKPerUnit).Mul(minorPerUnit(money.CZK)).Round(0).IntPart(), true
}

func minorPerUnit(c money.Currency) decimal.Decimal {
	return decimal.NewFromInt(10).Pow(decimal.NewFromInt(int64(c.Exponent())))
}

type InstrumentDetail struct {
	Instrument Instrument
	// Position is nil once everything is sold.
	Position  *Position
	Buys      []Buy
	Sales     []LotSale
	Dividends []Dividend
}

func (s *Service) Instrument(ctx context.Context, isin string) (*InstrumentDetail, error) {
	h, err := s.repo.history(ctx, &isin)
	if err != nil {
		return nil, err
	}
	instrument, ok := h.instruments[isin]
	if !ok {
		return nil, ErrNotFound
	}
	positions, err := s.positions(ctx, h)
	if err != nil {
		return nil, err
	}
	d := &InstrumentDetail{Instrument: instrument, Buys: h.buys, Sales: h.sales, Dividends: h.dividends}
	if len(positions) == 1 {
		d.Position = &positions[0]
	}
	return d, nil
}

type CandleSeries struct {
	// Symbol is nil without a Yahoo symbol; there are no candles then.
	Symbol *string
	// Currency is the quote's, which may differ from the trades'.
	Currency money.Currency
	Candles  []Candle
}

// Candles are the instrument's daily prices from the given day.
func (s *Service) Candles(ctx context.Context, isin string, from time.Time) (*CandleSeries, error) {
	h, err := s.repo.history(ctx, &isin)
	if err != nil {
		return nil, err
	}
	instrument, ok := h.instruments[isin]
	if !ok {
		return nil, ErrNotFound
	}
	series := &CandleSeries{Symbol: instrument.YahooSymbol, Currency: instrument.Currency, Candles: []Candle{}}
	if instrument.YahooSymbol == nil {
		return series, nil
	}
	quoted, err := s.repo.seriesCurrency(ctx, *instrument.YahooSymbol)
	if err != nil {
		return nil, err
	}
	if quoted != nil {
		series.Currency = *quoted
	}
	series.Candles, err = s.repo.candles(ctx, *instrument.YahooSymbol, from)
	return series, err
}

var yahooSymbolPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.\-^=]{0,19}$`)

// SetYahooSymbol takes "" to remove the symbol.
func (s *Service) SetYahooSymbol(ctx context.Context, isin, symbol string) error {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return s.repo.SetYahooSymbol(ctx, isin, nil)
	}
	if !yahooSymbolPattern.MatchString(symbol) {
		return ErrInvalidSymbol
	}
	return s.repo.SetYahooSymbol(ctx, isin, &symbol)
}
