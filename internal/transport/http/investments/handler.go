package investments

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/investment"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

const defaultCandleMonths = 12

type Handler struct {
	service   *investment.Service
	refresher *investment.Refresher
	clock     clock.Clock
}

func NewHandler(service *investment.Service, refresher *investment.Refresher, c clock.Clock) *Handler {
	return &Handler{service: service, refresher: refresher, clock: c}
}

type Instrument struct {
	ISIN        string  `json:"isin" example:"US0378331005"`
	Ticker      string  `json:"ticker" doc:"Empty when no statement named it yet."`
	Name        string  `json:"name"`
	Currency    string  `json:"currency" doc:"Of the user's own trades in it."`
	YahooSymbol *string `json:"yahoo_symbol,omitempty" doc:"Absent: no prices, the position is valued at cost."`
}

type Lot struct {
	AccountID  uuid.UUID     `json:"account_id"`
	BoughtOn   string        `json:"bought_on" format:"date"`
	Quantity   string        `json:"quantity" doc:"Decimal, may be fractional." example:"1.25"`
	Cost       money.Amount  `json:"cost"`
	CostCZK    *money.Amount `json:"cost_czk,omitempty"`
	ExemptFrom string        `json:"exempt_from" format:"date" doc:"First sale day on which the lot passes the three-year time test."`
}

type Quote struct {
	Day      string `json:"day" format:"date"`
	Close    string `json:"close" doc:"Decimal."`
	Currency string `json:"currency" doc:"The quote's, which may differ from the trades'."`
}

type Position struct {
	Instrument        Instrument    `json:"instrument"`
	Quantity          string        `json:"quantity"`
	Lots              []Lot         `json:"lots" doc:"Open lots, oldest first."`
	Cost              *money.Amount `json:"cost,omitempty" doc:"In the lots' currency; absent when lots were bought in different currencies."`
	CostCZK           *money.Amount `json:"cost_czk,omitempty" doc:"Absent when a lot has no CZK cost."`
	Quote             *Quote        `json:"quote,omitempty" doc:"Absent without a usable price."`
	Value             *money.Amount `json:"value,omitempty"`
	ValueCZK          *money.Amount `json:"value_czk,omitempty" doc:"Absent without a price or an exchange rate."`
	DividendsNetCZK   money.Amount  `json:"dividends_net_czk"`
	IncompleteHistory bool          `json:"incomplete_history" doc:"Some sold lot has no purchase in the imported statements."`
	ExemptQuantity    string        `json:"exempt_quantity" doc:"Held long enough to pass the time test today."`
	NextExemptOn      *string       `json:"next_exempt_on,omitempty" format:"date"`
}

type PriceStatus struct {
	Enabled    bool       `json:"enabled" doc:"False when the server fetches no market data."`
	Running    bool       `json:"running"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Failures   int        `json:"failures" doc:"Sources or symbols the last refresh could not fetch."`
}

type Portfolio struct {
	Positions         []Position   `json:"positions"`
	ValueCZK          money.Amount `json:"value_czk" doc:"Sum over positions that have both a CZK value and a CZK cost."`
	CostCZK           money.Amount `json:"cost_czk"`
	Unvalued          int          `json:"unvalued" doc:"Positions left out of the totals."`
	RatesOn           *string      `json:"rates_on,omitempty" format:"date" doc:"Day of the ČNB rates used."`
	IncompleteHistory []Instrument `json:"incomplete_history" doc:"Instruments with sales whose purchase was never imported; import older statements."`
	Prices            PriceStatus  `json:"prices"`
}

type portfolioOutput struct {
	Body Portfolio
}

type YearGains struct {
	Year               int          `json:"year"`
	Lots               int          `json:"lots"`
	MissingCZK         int          `json:"missing_czk" doc:"Lots without CZK values, left out of the sums."`
	ProceedsCZK        money.Amount `json:"proceeds_czk"`
	CostCZK            money.Amount `json:"cost_czk"`
	GainCZK            money.Amount `json:"gain_czk"`
	ExemptGainCZK      money.Amount `json:"exempt_gain_czk" doc:"From lots that passed the time test."`
	TaxableGainCZK     money.Amount `json:"taxable_gain_czk"`
	TaxableProceedsCZK money.Amount `json:"taxable_proceeds_czk" doc:"What the yearly 100 000 Kč exemption is tested on."`
	DividendsGrossCZK  money.Amount `json:"dividends_gross_czk"`
	DividendsTaxCZK    money.Amount `json:"dividends_tax_czk"`
	DividendsNetCZK    money.Amount `json:"dividends_net_czk"`
}

type SoldLot struct {
	Instrument  Instrument    `json:"instrument"`
	AccountID   uuid.UUID     `json:"account_id"`
	BoughtOn    string        `json:"bought_on" format:"date"`
	SoldOn      string        `json:"sold_on" format:"date"`
	Quantity    string        `json:"quantity"`
	Proceeds    money.Amount  `json:"proceeds"`
	Cost        money.Amount  `json:"cost"`
	Gain        money.Amount  `json:"gain"`
	ProceedsCZK *money.Amount `json:"proceeds_czk,omitempty"`
	CostCZK     *money.Amount `json:"cost_czk,omitempty"`
	GainCZK     *money.Amount `json:"gain_czk,omitempty"`
	Exempt      bool          `json:"exempt" doc:"Held more than three years."`
}

type realizedOutput struct {
	Body struct {
		Years []YearGains `json:"years" doc:"Newest first."`
		Lots  []SoldLot   `json:"lots" doc:"Newest sale first."`
	}
}

type isinInput struct {
	ISIN string `path:"isin" pattern:"^[A-Z]{2}[A-Z0-9]{9}[0-9]$"`
}

type Trade struct {
	AccountID uuid.UUID     `json:"account_id"`
	Kind      string        `json:"kind" enum:"buy,dividend"`
	Day       string        `json:"day" format:"date"`
	Quantity  *string       `json:"quantity,omitempty" doc:"Buys only."`
	Net       money.Amount  `json:"net" doc:"A buy's cost with fees; a dividend after tax."`
	NetCZK    *money.Amount `json:"net_czk,omitempty"`
}

type InstrumentDetail struct {
	Instrument Instrument `json:"instrument"`
	Position   *Position  `json:"position,omitempty" doc:"Absent once everything is sold."`
	Trades     []Trade    `json:"trades" doc:"Buys and dividends, oldest first."`
	Sales      []SoldLot  `json:"sales" doc:"Sold lots, oldest first."`
}

type instrumentOutput struct {
	Body InstrumentDetail
}

type candlesInput struct {
	ISIN string `path:"isin" pattern:"^[A-Z]{2}[A-Z0-9]{9}[0-9]$"`
	From string `query:"from" format:"date" doc:"First day; a year back when omitted."`
}

type Candle struct {
	Day    string  `json:"day" format:"date"`
	Open   *string `json:"open,omitempty"`
	High   *string `json:"high,omitempty"`
	Low    *string `json:"low,omitempty"`
	Close  string  `json:"close"`
	Volume *int64  `json:"volume,omitempty"`
}

type candlesOutput struct {
	Body struct {
		Symbol   *string  `json:"symbol,omitempty" doc:"Absent: the instrument has no Yahoo symbol, so no candles."`
		Currency string   `json:"currency" doc:"The quote's currency."`
		Candles  []Candle `json:"candles" doc:"Daily, oldest first."`
	}
}

type symbolInput struct {
	ISIN string `path:"isin" pattern:"^[A-Z]{2}[A-Z0-9]{9}[0-9]$"`
	Body struct {
		Symbol string `json:"symbol" maxLength:"20" doc:"Yahoo Finance symbol, e.g. EUNL.DE; empty removes it." example:"EUNL.DE"`
	}
}

type priceStatusOutput struct {
	Body PriceStatus
}

func (h *Handler) Register(api huma.API) {
	read := func(op huma.Operation) httpapi.Op {
		op.Tags = []string{"Investments"}
		return httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeInvestmentsRead, Operation: op}
	}
	httpapi.Register(api, read(huma.Operation{
		OperationID: "get-portfolio", Method: http.MethodGet, Path: "/api/v1/investments/portfolio",
		Summary: "Open positions with their lots, value and the three-year time test",
	}), h.portfolio)
	httpapi.Register(api, read(huma.Operation{
		OperationID: "get-realized-gains", Method: http.MethodGet, Path: "/api/v1/investments/realized",
		Summary: "Sold lots and dividends per year, split into exempt and taxable gains",
		Description: "A lot is exempt when held more than three years. Amounts are in CZK as the statements convert them; " +
			"this is an overview, not a tax return.",
	}), h.realized)
	httpapi.Register(api, read(huma.Operation{
		OperationID: "get-instrument", Method: http.MethodGet, Path: "/api/v1/investments/instruments/{isin}",
		Summary: "One instrument: its position, trades and sold lots", Errors: []int{404},
	}), h.instrument)
	httpapi.Register(api, read(huma.Operation{
		OperationID: "list-candles", Method: http.MethodGet, Path: "/api/v1/investments/instruments/{isin}/candles",
		Summary: "Daily prices of an instrument", Errors: []int{404, 422},
	}), h.candles)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "set-yahoo-symbol", Method: http.MethodPut, Path: "/api/v1/investments/instruments/{isin}/yahoo-symbol",
		Tags: []string{"Investments"}, Summary: "Set the Yahoo Finance symbol prices are fetched under",
		Description: "The symbol is the signed-in user's own; other holders keep theirs. Prices are fetched in the background.",
		Errors:      []int{404, 422},
	}}, h.setSymbol)
	httpapi.Register(api, read(huma.Operation{
		OperationID: "get-price-status", Method: http.MethodGet, Path: "/api/v1/investments/prices/status",
		Summary: "Whether prices are being fetched and when they last were",
	}), h.status)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "refresh-prices", Method: http.MethodPost, Path: "/api/v1/investments/prices/refresh",
		Tags: []string{"Investments"}, Summary: "Fetch prices and exchange rates now, in the background",
		Description:   "A request within a minute of the last refresh is ignored.",
		DefaultStatus: http.StatusAccepted,
	}}, h.refresh)
}

func (h *Handler) portfolio(ctx context.Context, _ *struct{}) (*portfolioOutput, error) {
	p, err := h.service.Portfolio(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := Portfolio{
		Positions: make([]Position, 0, len(p.Positions)), ValueCZK: czk(p.ValueCZKMinor), CostCZK: czk(p.CostCZKMinor),
		Unvalued: p.Unvalued, RatesOn: dto.DatePtr(p.RatesOn), IncompleteHistory: make([]Instrument, 0, len(p.IncompleteHistory)),
		Prices: h.priceStatus(),
	}
	for _, pos := range p.Positions {
		out.Positions = append(out.Positions, positionDTO(pos))
	}
	for _, i := range p.IncompleteHistory {
		out.IncompleteHistory = append(out.IncompleteHistory, instrumentDTO(i))
	}
	httpapi.CountResults(ctx, len(out.Positions))
	return &portfolioOutput{Body: out}, nil
}

func (h *Handler) realized(ctx context.Context, _ *struct{}) (*realizedOutput, error) {
	r, err := h.service.Realized(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &realizedOutput{}
	out.Body.Years = make([]YearGains, 0, len(r.Years))
	for _, y := range r.Years {
		out.Body.Years = append(out.Body.Years, YearGains{
			Year: y.Year, Lots: y.Lots, MissingCZK: y.MissingCZK, ProceedsCZK: czk(y.ProceedsCZKMinor),
			CostCZK: czk(y.CostCZKMinor), GainCZK: czk(y.GainCZKMinor), ExemptGainCZK: czk(y.ExemptGainCZKMinor),
			TaxableGainCZK: czk(y.TaxableGainCZKMinor), TaxableProceedsCZK: czk(y.TaxableProceedsCZKMinor),
			DividendsGrossCZK: czk(y.DividendsGrossCZKMinor), DividendsTaxCZK: czk(y.DividendsTaxCZKMinor),
			DividendsNetCZK: czk(y.DividendsNetCZKMinor),
		})
	}
	out.Body.Lots = make([]SoldLot, 0, len(r.Lots))
	for _, l := range r.Lots {
		out.Body.Lots = append(out.Body.Lots, soldLotDTO(l))
	}
	httpapi.CountResults(ctx, len(out.Body.Lots))
	return out, nil
}

func (h *Handler) instrument(ctx context.Context, in *isinInput) (*instrumentOutput, error) {
	d, err := h.service.Instrument(ctx, in.ISIN)
	if errors.Is(err, investment.ErrNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := InstrumentDetail{Instrument: instrumentDTO(d.Instrument), Trades: []Trade{}, Sales: make([]SoldLot, 0, len(d.Sales))}
	if d.Position != nil {
		pos := positionDTO(*d.Position)
		out.Position = &pos
	}
	for _, b := range d.Buys {
		q := b.Quantity.String()
		out.Trades = append(out.Trades, Trade{AccountID: b.AccountID, Kind: "buy", Day: dto.Date(b.TradedOn), Quantity: &q,
			Net: money.New(b.NetMinor, b.Currency), NetCZK: czkPtr(b.NetCZKMinor)})
	}
	for _, dv := range d.Dividends {
		out.Trades = append(out.Trades, Trade{AccountID: dv.AccountID, Kind: "dividend", Day: dto.Date(dv.PaidOn),
			Net: money.New(dv.NetMinor, dv.Currency), NetCZK: czkPtr(dv.NetCZKMinor)})
	}
	sortTrades(out.Trades)
	for _, s := range d.Sales {
		out.Sales = append(out.Sales, soldLotDTO(investment.SoldLot{LotSale: s, Instrument: d.Instrument,
			Exempt: investment.PassesTimeTest(s.BoughtOn, s.SoldOn)}))
	}
	return &instrumentOutput{Body: out}, nil
}

func (h *Handler) candles(ctx context.Context, in *candlesInput) (*candlesOutput, error) {
	from := clock.Today(h.clock).AddDate(0, -defaultCandleMonths, 0)
	if in.From != "" {
		parsed, err := dto.ParseDate(in.From, "query.from")
		if err != nil {
			return nil, err
		}
		from = *parsed
	}
	series, err := h.service.Candles(ctx, in.ISIN, from)
	if errors.Is(err, investment.ErrNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &candlesOutput{}
	out.Body.Symbol, out.Body.Currency = series.Symbol, string(series.Currency)
	out.Body.Candles = make([]Candle, 0, len(series.Candles))
	for _, c := range series.Candles {
		out.Body.Candles = append(out.Body.Candles, Candle{Day: dto.Date(c.Day), Open: nullDecimal(c.Open),
			High: nullDecimal(c.High), Low: nullDecimal(c.Low), Close: c.Close.String(), Volume: c.Volume})
	}
	httpapi.CountResults(ctx, len(out.Body.Candles))
	return out, nil
}

func (h *Handler) setSymbol(ctx context.Context, in *symbolInput) (*instrumentOutput, error) {
	err := h.service.SetYahooSymbol(ctx, in.ISIN, in.Body.Symbol)
	switch {
	case errors.Is(err, investment.ErrNotFound):
		return nil, apierr.NotFound()
	case errors.Is(err, investment.ErrInvalidSymbol):
		return nil, apierr.Unprocessable(apierr.CodeInvalidSymbol)
	case err != nil:
		return nil, apierr.Internal(ctx, err)
	}
	h.refresher.Trigger()
	return h.instrument(ctx, &isinInput{ISIN: in.ISIN})
}

func (h *Handler) refresh(_ context.Context, _ *struct{}) (*priceStatusOutput, error) {
	h.refresher.RequestRefresh()
	return &priceStatusOutput{Body: h.priceStatus()}, nil
}

func (h *Handler) status(_ context.Context, _ *struct{}) (*priceStatusOutput, error) {
	return &priceStatusOutput{Body: h.priceStatus()}, nil
}

func (h *Handler) priceStatus() PriceStatus {
	s := h.refresher.Status()
	return PriceStatus{Enabled: h.refresher.Enabled(), Running: s.Running, FinishedAt: s.FinishedAt, Failures: s.Failures}
}

func instrumentDTO(i investment.Instrument) Instrument {
	return Instrument{ISIN: i.ISIN, Ticker: i.Ticker, Name: i.Name, Currency: string(i.Currency), YahooSymbol: i.YahooSymbol}
}

func positionDTO(p investment.Position) Position {
	out := Position{
		Instrument: instrumentDTO(p.Instrument), Quantity: p.Quantity.String(), Lots: make([]Lot, 0, len(p.Lots)),
		CostCZK:  czkPtr(p.CostCZKMinor),
		ValueCZK: czkPtr(p.ValueCZKMinor), DividendsNetCZK: czk(p.DividendsNetCZKMinor),
		IncompleteHistory: p.IncompleteHistory, ExemptQuantity: p.ExemptQuantity.String(), NextExemptOn: dto.DatePtr(p.NextExemptOn),
	}
	for _, l := range p.Lots {
		out.Lots = append(out.Lots, Lot{AccountID: l.AccountID, BoughtOn: dto.Date(l.BoughtOn), Quantity: l.Quantity.String(),
			Cost: money.New(l.CostMinor, l.Currency), CostCZK: czkPtr(l.CostCZKMinor), ExemptFrom: dto.Date(investment.ExemptFrom(l.BoughtOn))})
	}
	if !p.MixedCurrencies {
		cost := money.New(p.CostMinor, p.CostCurrency)
		out.Cost = &cost
	}
	if p.Quote != nil {
		out.Quote = &Quote{Day: dto.Date(p.Quote.Day), Close: p.Quote.Close.String(), Currency: string(p.Quote.Currency)}
		v := money.New(*p.ValueMinor, p.Quote.Currency)
		out.Value = &v
	}
	return out
}

func soldLotDTO(l investment.SoldLot) SoldLot {
	return SoldLot{
		Instrument: instrumentDTO(l.Instrument), AccountID: l.AccountID, BoughtOn: dto.Date(l.BoughtOn), SoldOn: dto.Date(l.SoldOn),
		Quantity: l.Quantity.String(), Proceeds: money.New(l.ProceedsMinor, l.Currency), Cost: money.New(l.CostMinor, l.Currency),
		Gain: money.New(l.GainMinor, l.Currency), ProceedsCZK: czkPtr(l.ProceedsCZKMinor), CostCZK: czkPtr(l.CostCZKMinor),
		GainCZK: czkPtr(l.GainCZKMinor), Exempt: l.Exempt,
	}
}

func sortTrades(trades []Trade) {
	slices.SortStableFunc(trades, func(a, b Trade) int { return strings.Compare(a.Day, b.Day) })
}

func czk(minor int64) money.Amount { return money.New(minor, money.CZK) }

func czkPtr(minor *int64) *money.Amount {
	if minor == nil {
		return nil
	}
	a := czk(*minor)
	return &a
}

func nullDecimal(d decimal.NullDecimal) *string {
	if !d.Valid {
		return nil
	}
	s := d.Decimal.String()
	return &s
}
