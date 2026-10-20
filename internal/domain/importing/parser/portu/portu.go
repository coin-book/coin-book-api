// Package portu parses Portu's transaction history CSV: trades, dividends and currency exchanges of
// every portfolio of one Portu account (coin-book/importer-formaty.md, section Portu; ADR 212).
package portu

import (
	"bytes"
	"encoding/csv"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/money"
)

const Format statement.Format = "portu-transactions"

const version = 1

// ExternalRef is the one Portu account a user has; its portfolios are kept apart only for lots.
const ExternalRef = "portu:investments"

// cashPosition is the ISIN Portu gives crowns held in a reserve portfolio: cash, not an instrument.
const cashPosition = "CZK000000001"

var header = []string{"Datum", "Název", "Typ", "Symbol", "ISIN", "Kusy / Pozice", "Popis", "Cena", "Hodnota", "Měna",
	"Srážková daň", "Hrubá výše dividendy"}

const (
	colDate = iota
	colPortfolio
	colType
	colSymbol
	colISIN
	colQuantity
	colDescription
	colPrice
	colValue
	colCurrency
	colWithholding
	colGrossDividend
)

// A household's export has a few thousand rows; the bounds keep a crafted one from occupying
// the server: moving lots between portfolios again and again would cost rows × lots.
const (
	maxRows     = 50_000
	maxLotSteps = 1_000_000
)

// A trade in another currency is valued in CZK at the rate of Portu's own exchange that day, or
// the nearest within this many days; further away the CZK value stays unknown.
const rateWindowDays = 7

var (
	utf8BOM = []byte{0xEF, 0xBB, 0xBF}
	// Plain numbers with a decimal comma; thousands separators are removed first. Exponents are
	// refused: decimal parsing would take 1e-2000000000 and spend minutes scaling it.
	plainNumber  = regexp.MustCompile(`^-?[0-9]{1,13}(,[0-9]{1,10})?$`)
	currencyPair = regexp.MustCompile(`^([A-Z]{3})/([A-Z]{3})$`)
	// Portu exchanged through swaps in 2022: "9.3.2022 Swap USDCZK", "… Swap EURCZK R".
	currencySwap = regexp.MustCompile(`Swap ([A-Z]{3})([A-Z]{3})\b`)
	// Exchange suffixes of Portu's symbols ("CSPX LN") and the Yahoo Finance ones they stand for.
	yahooSuffix = map[string]string{"US": "", "LN": ".L", "GY": ".DE", "CP": ".PR"}
)

var (
	ErrNotPortu     = eris.New("portu: the file is not a Portu transaction history")
	ErrInvalidValue = eris.New("portu: a cell holds no valid value")
)

type Parser struct{}

func (Parser) Format() statement.Format { return Format }

func (Parser) Version() int { return version }

func (Parser) Detect(head []byte) statement.Confidence {
	first, _, _ := bytes.Cut(bytes.TrimPrefix(head, utf8BOM), []byte("\n"))
	if strings.TrimRight(string(first), "\r") == strings.Join(header, ";") {
		return statement.Certain
	}
	return statement.NoMatch
}

type row struct {
	line   int
	cells  []string
	date   time.Time
	amount int64
	curr   money.Currency
}

func (r row) cell(i int) string { return strings.TrimSpace(r.cells[i]) }

func (r row) invalid(column int) error {
	return eris.Wrapf(ErrInvalidValue, "line %d, %s", r.line, header[column])
}

func (Parser) Parse(rd io.Reader) (*statement.Statement, error) {
	content, err := io.ReadAll(rd)
	if err != nil {
		return nil, eris.Wrap(err, "portu: read")
	}
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(content, utf8BOM)))
	reader.Comma = ';'
	reader.FieldsPerRecord = len(header)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, eris.Wrap(ErrNotPortu, err.Error())
	}
	if len(records) == 0 || !slices.Equal(records[0], header) {
		return nil, ErrNotPortu
	}
	if len(records) > maxRows+1 {
		return nil, eris.Wrapf(ErrInvalidValue, "more than %d rows", maxRows)
	}
	rows := make([]row, 0, len(records)-1)
	for i, cells := range records[1:] {
		r := row{line: i + 2, cells: cells}
		if r.date, err = time.Parse("2.1.2006", r.cell(colDate)); err != nil {
			return nil, r.invalid(colDate)
		}
		var ok bool
		if r.curr, ok = currency(r.cell(colCurrency)); !ok {
			return nil, r.invalid(colCurrency)
		}
		if r.amount, err = r.money(colValue); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	// Newest first in the file; lots are matched oldest first. Within a day the file's order reversed.
	slices.Reverse(rows)
	slices.SortStableFunc(rows, func(a, b row) int { return a.date.Compare(b.date) })

	p := &replay{rates: exchangeRates(rows), lots: map[lotKey][]lot{}}
	for _, r := range rows {
		if err := p.consume(r); err != nil {
			return nil, err
		}
	}
	if len(rows) == 0 {
		return nil, ErrNotPortu
	}
	from, to := rows[0].date, rows[len(rows)-1].date
	ref := ExternalRef
	return &statement.Statement{
		Format: Format, Version: version, PeriodFrom: &from, PeriodTo: &to,
		Accounts: []*statement.Account{{
			Ref:         statement.AccountRef{Institution: account.Portu, Kind: account.Broker, Currency: money.CZK, ExternalRef: &ref, Name: "Portu"},
			Investments: &p.out,
		}},
	}, nil
}

func currency(raw string) (money.Currency, bool) {
	c := money.Currency(raw)
	return c, c.Valid()
}

// maxMinor keeps conversions within int64 with room to sum them.
var maxMinor = decimal.NewFromInt(math.MaxInt64 / 1000)

func exponent(c money.Currency) int32 {
	return int32(c.Exponent()) //nolint:gosec // G115: an ISO 4217 exponent is 0 to 4
}

func number(raw string) (decimal.Decimal, bool) {
	cleaned := strings.NewReplacer(" ", "", " ", "", " ", "").Replace(raw)
	if !plainNumber.MatchString(cleaned) {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(strings.Replace(cleaned, ",", ".", 1))
	return d, err == nil
}

// money reads an amount in the row's currency into minor units; an empty cell is zero.
func (r row) money(column int) (int64, error) {
	raw := r.cell(column)
	if raw == "" {
		return 0, nil
	}
	d, ok := number(raw)
	if !ok {
		return 0, r.invalid(column)
	}
	return d.Shift(exponent(r.curr)).Round(0).IntPart(), nil
}

func (r row) quantity() (decimal.Decimal, error) {
	d, ok := number(r.cell(colQuantity))
	if !ok || !d.IsPositive() {
		return d, r.invalid(colQuantity)
	}
	return d, nil
}

func (r row) security() (statement.Security, error) {
	isin := r.cell(colISIN)
	if !statement.ValidISIN(isin) {
		return statement.Security{}, r.invalid(colISIN)
	}
	symbol := r.cell(colSymbol)
	ticker, exchange, _ := strings.Cut(symbol, " ")
	yahoo := ""
	if suffix, ok := yahooSuffix[exchange]; ok && ticker != "" {
		// Yahoo writes share classes with a dash: BRK.B is BRK-B.
		yahoo = strings.ReplaceAll(ticker, ".", "-") + suffix
	}
	return statement.NewSecurity(isin, ticker, symbol, yahoo), nil
}

type rateKey struct {
	day      time.Time
	currency money.Currency
}

type exchanged struct{ czk, foreign decimal.Decimal }

// exchangeRates are CZK per unit of each currency Portu bought or sold for crowns that day, from
// its own exchanges and swaps against crowns; cross exchanges (EUR/USD) are left out.
func exchangeRates(rows []row) map[rateKey]decimal.Decimal {
	sums := map[rateKey]*exchanged{}
	for _, r := range rows {
		if !strings.HasPrefix(r.cell(colType), "Forex ") {
			continue
		}
		pair := currencyPair.FindStringSubmatch(r.cell(colDescription))
		if pair == nil {
			pair = currencySwap.FindStringSubmatch(r.cell(colDescription))
		}
		if pair == nil || (pair[1] != "CZK" && pair[2] != "CZK") {
			continue
		}
		foreign := money.Currency(pair[1])
		if foreign == money.CZK {
			foreign = money.Currency(pair[2])
		}
		key := rateKey{r.date, foreign}
		if sums[key] == nil {
			sums[key] = &exchanged{}
		}
		amount := decimal.NewFromInt(r.amount).Abs().Shift(-exponent(r.curr))
		if r.curr == money.CZK {
			sums[key].czk = sums[key].czk.Add(amount)
		} else {
			sums[key].foreign = sums[key].foreign.Add(amount)
		}
	}
	rates := map[rateKey]decimal.Decimal{}
	for key, s := range sums {
		if s.czk.IsPositive() && s.foreign.IsPositive() {
			rates[key] = s.czk.Div(s.foreign)
		}
	}
	return rates
}

type lotKey struct {
	portfolio string
	isin      string
}

// lot is what is left of one purchase; cost is in the purchase's currency.
type lot struct {
	boughtOn time.Time
	quantity decimal.Decimal
	currency money.Currency
	cost     decimal.Decimal
	costCZK  *decimal.Decimal
}

// replay walks the history oldest first, keeping each portfolio's open lots.
type replay struct {
	rates map[rateKey]decimal.Decimal
	lots  map[lotKey][]lot
	out   statement.Investments
	// steps counts lots visited taking and placing them across the file, against maxLotSteps.
	steps int
}

// toCZK converts at Portu's own rate of the nearest exchange day; nil when there was none near.
func (p *replay) toCZK(minor int64, c money.Currency, day time.Time) *int64 {
	if c == money.CZK {
		return &minor
	}
	for offset := range rateWindowDays + 1 {
		for _, d := range []time.Time{day.AddDate(0, 0, -offset), day.AddDate(0, 0, offset)} {
			if rate, ok := p.rates[rateKey{d, c}]; ok {
				units := decimal.NewFromInt(minor).Shift(-exponent(c))
				inCZK := units.Mul(rate).Shift(exponent(money.CZK)).Round(0)
				if inCZK.Abs().GreaterThan(maxMinor) {
					return nil
				}
				v := inCZK.IntPart()
				return &v
			}
		}
	}
	return nil
}

func (p *replay) consume(r row) error {
	kind := r.cell(colType)
	if r.cell(colISIN) == cashPosition {
		return nil
	}
	switch {
	case strings.HasPrefix(kind, "Nákup – "):
		return p.buy(r)
	case strings.HasPrefix(kind, "Prodej – "):
		return p.sell(r)
	case strings.HasPrefix(kind, "Dividenda – "):
		return p.dividend(r)
	case strings.HasPrefix(kind, "Převod do ") && r.cell(colISIN) != "":
		return p.move(r, transferTarget(kind, r.cell(colSymbol)))
	default:
		// Deposits, exchanges, fees, cash interest and cash moves between portfolios: the account
		// is valued by its positions, and deposits pair with the bank's payments (ADR 206).
		return nil
	}
}

func (p *replay) buy(r row) error {
	sec, err := r.security()
	if err != nil {
		return err
	}
	qty, err := r.quantity()
	if err != nil {
		return err
	}
	gross := abs(r.amount)
	b := statement.Buy{Line: r.line, TradedOn: r.date, Security: sec, Quantity: qty, Currency: r.curr,
		GrossMinor: gross, NetMinor: gross, GrossCZKMinor: p.toCZK(gross, r.curr, r.date)}
	b.NetCZKMinor = b.GrossCZKMinor
	b.IdentityKey = identityKey("buy", r.date, sec.ISIN, r.cell(colPortfolio), qty.String(), itoa(gross))
	p.out.Buys = append(p.out.Buys, b)

	l := lot{boughtOn: r.date, quantity: qty, currency: r.curr, cost: decimal.NewFromInt(gross)}
	if b.GrossCZKMinor != nil {
		c := decimal.NewFromInt(*b.GrossCZKMinor)
		l.costCZK = &c
	}
	key := lotKey{r.cell(colPortfolio), sec.ISIN}
	p.lots[key] = append(p.lots[key], l)
	return nil
}

// take removes qty from the portfolio's lots, oldest first; missing is what no lot covered.
func (p *replay) take(key lotKey, qty decimal.Decimal) (taken []lot, missing decimal.Decimal, err error) {
	lots := p.lots[key]
	for qty.IsPositive() && len(lots) > 0 {
		if p.steps++; p.steps > maxLotSteps {
			return nil, qty, eris.Wrap(ErrInvalidValue, "too many lots to match")
		}
		l := lots[0]
		part := decimal.Min(qty, l.quantity)
		share := part.Div(l.quantity)
		piece := lot{boughtOn: l.boughtOn, quantity: part, currency: l.currency, cost: l.cost.Mul(share).Round(0)}
		if l.costCZK != nil {
			c := l.costCZK.Mul(share).Round(0)
			piece.costCZK = &c
			rest := l.costCZK.Sub(c)
			l.costCZK = &rest
		}
		taken = append(taken, piece)
		l.quantity = l.quantity.Sub(part)
		l.cost = l.cost.Sub(piece.cost)
		qty = qty.Sub(part)
		if l.quantity.IsPositive() {
			lots[0] = l
		} else {
			lots = lots[1:]
		}
	}
	p.lots[key] = lots
	return taken, qty, nil
}

// sell matches the sale to the portfolio's lots first in, first out (Portu names no lot); a part
// no imported purchase covers is a lot bought the day it was sold, which fails the time test.
func (p *replay) sell(r row) error {
	sec, err := r.security()
	if err != nil {
		return err
	}
	qty, err := r.quantity()
	if err != nil {
		return err
	}
	proceeds := decimal.NewFromInt(abs(r.amount))
	taken, missing, err := p.take(lotKey{r.cell(colPortfolio), sec.ISIN}, qty)
	if err != nil {
		return err
	}
	if missing.IsPositive() {
		taken = append(taken, lot{boughtOn: r.date, quantity: missing, currency: r.curr})
	}
	var allotted decimal.Decimal
	for i, l := range taken {
		share := proceeds.Mul(l.quantity).Div(qty).Round(0)
		if i == len(taken)-1 {
			share = proceeds.Sub(allotted)
		}
		allotted = allotted.Add(share)
		s := statement.LotSale{Line: r.line, SoldOn: r.date, BoughtOn: l.boughtOn, Security: sec, Quantity: l.quantity,
			Currency: r.curr, ProceedsMinor: share.IntPart(), CostMinor: l.cost.IntPart()}
		s.GainMinor = s.ProceedsMinor - s.CostMinor
		s.ProceedsCZKMinor = p.toCZK(s.ProceedsMinor, r.curr, r.date)
		if s.ProceedsCZKMinor != nil && l.costCZK != nil {
			cost := l.costCZK.IntPart()
			gain := *s.ProceedsCZKMinor - cost
			s.CostCZKMinor, s.GainCZKMinor = &cost, &gain
		}
		s.IdentityKey = identityKey("lot", r.date, sec.ISIN, r.cell(colPortfolio), l.boughtOn.Format(time.DateOnly),
			l.quantity.String(), itoa(s.ProceedsMinor))
		p.out.LotSales = append(p.out.LotSales, s)
	}
	return nil
}

// transferTarget reads the portfolio out of "Převod do Důchod – CSPX LN – 2 kusy"; a portfolio's
// own name may hold a dash too ("Oliver – první"), so the cut is at the instrument's symbol.
func transferTarget(kind, symbol string) string {
	target := strings.TrimPrefix(kind, "Převod do ")
	if i := strings.Index(target, " – "+symbol+" – "); symbol != "" && i >= 0 {
		target = target[:i]
	}
	return strings.TrimSpace(target)
}

// move hands lots to another portfolio of the account: not a sale, so they keep their purchase day.
func (p *replay) move(r row, target string) error {
	sec, err := r.security()
	if err != nil {
		return err
	}
	qty, err := r.quantity()
	if err != nil {
		return err
	}
	taken, missing, err := p.take(lotKey{r.cell(colPortfolio), sec.ISIN}, qty)
	if err != nil {
		return err
	}
	if missing.IsPositive() {
		return eris.Wrapf(ErrInvalidValue, "line %d: moves more %s than the portfolio holds", r.line, sec.ISIN)
	}
	key := lotKey{target, sec.ISIN}
	p.lots[key] = append(p.lots[key], taken...)
	// Placing the pieces among the target's lots is work too: many tiny moves into a long list add up.
	if p.steps += len(p.lots[key]); p.steps > maxLotSteps {
		return eris.Wrap(ErrInvalidValue, "too many lots to match")
	}
	slices.SortStableFunc(p.lots[key], func(a, b lot) int { return a.boughtOn.Compare(b.boughtOn) })
	return nil
}

func (p *replay) dividend(r row) error {
	sec, err := r.security()
	if err != nil {
		return err
	}
	gross, err := r.money(colGrossDividend)
	if err != nil {
		return err
	}
	tax, err := r.money(colWithholding)
	if err != nil {
		return err
	}
	net := r.amount
	if gross == 0 {
		gross = net + tax
	}
	d := statement.Dividend{Line: r.line, PaidOn: r.date, Security: sec, Currency: r.curr, GrossMinor: gross, NetMinor: net,
		GrossCZKMinor: p.toCZK(gross, r.curr, r.date), NetCZKMinor: p.toCZK(net, r.curr, r.date)}
	if taxCZK := p.toCZK(tax, r.curr, r.date); taxCZK != nil {
		d.TaxCZKMinor = *taxCZK
	}
	d.IdentityKey = identityKey("dividend", r.date, sec.ISIN, r.cell(colPortfolio), itoa(gross))
	p.out.Dividends = append(p.out.Dividends, d)
	return nil
}

func identityKey(kind string, day time.Time, isin string, parts ...string) string {
	return strings.Join(append([]string{kind, day.Format(time.DateOnly), isin}, parts...), "|")
}

func itoa(v int64) string { return decimal.NewFromInt(v).String() }

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
