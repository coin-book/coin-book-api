package investment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/platform/money"
)

// Rates are CZK per unit of each currency, per day.
type Rates map[time.Time]map[money.Currency]decimal.Decimal

// ToCZK converts at the day's rate; false when there is none.
func (r Rates) ToCZK(minor int64, currency money.Currency, day time.Time) (int64, bool) {
	if currency == money.CZK {
		return minor, true
	}
	rate, ok := r[day][currency]
	if !ok {
		return 0, false
	}
	return toCZK(minor, currency, map[money.Currency]Rate{currency: {CZKPerUnit: rate}})
}

// RatesOn are the ČNB rates in force on each day. A day before the first rate the app fetched
// takes that first rate: the history of a currency held for long is better approximate than missing.
func (s *Service) RatesOn(ctx context.Context, days []time.Time, currencies []money.Currency) (Rates, error) {
	out := Rates{}
	for _, d := range days {
		out[d] = map[money.Currency]decimal.Decimal{}
	}
	codes := make([]string, 0, len(currencies))
	for _, c := range currencies {
		if c != money.CZK {
			codes = append(codes, string(c))
		}
	}
	if len(codes) == 0 || len(days) == 0 {
		return out, nil
	}
	rows, err := s.repo.pool.Query(ctx, `
		SELECT d.day, c.code, r.czk_per_unit::text
		FROM unnest($1::date[]) AS d (day) CROSS JOIN unnest($2::text[]) AS c (code)
		CROSS JOIN LATERAL (
			SELECT czk_per_unit FROM (
				(SELECT czk_per_unit, 0 AS pick FROM fx_rate WHERE currency = c.code AND day <= d.day ORDER BY day DESC LIMIT 1)
				UNION ALL
				(SELECT czk_per_unit, 1 FROM fx_rate WHERE currency = c.code ORDER BY day LIMIT 1)
			) candidates ORDER BY pick LIMIT 1
		) r`, days, codes)
	if err != nil {
		return nil, eris.Wrap(err, "rates on days")
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var code, value string
		if err := rows.Scan(&day, &code, &value); err != nil {
			return nil, eris.Wrap(err, "read rate on day")
		}
		rate, err := decimal.NewFromString(value)
		if err != nil {
			return nil, eris.Wrap(err, "read rate on day")
		}
		out[day][money.Currency(code)] = rate
	}
	return out, eris.Wrap(rows.Err(), "read rates on days")
}

// HoldingsValue is what the positions of each broker account were worth on a day, in CZK.
type HoldingsValue struct {
	ByAccount map[uuid.UUID]int64
	// Unvalued counts positions without a price or a rate that day, left out.
	Unvalued int
}

// ValuesOn prices the positions held at the end of each day at that day's last close.
func (s *Service) ValuesOn(ctx context.Context, days []time.Time) (map[time.Time]HoldingsValue, error) {
	h, err := s.repo.history(ctx, nil)
	if err != nil {
		return nil, err
	}
	var symbols []string
	for _, i := range h.instruments {
		if i.YahooSymbol != nil {
			symbols = append(symbols, *i.YahooSymbol)
		}
	}
	closes, err := s.repo.closesOn(ctx, symbols, days)
	if err != nil {
		return nil, err
	}
	var currencies []money.Currency
	for _, byDay := range closes {
		for _, q := range byDay {
			currencies = append(currencies, q.Currency)
		}
	}
	rates, err := s.RatesOn(ctx, days, currencies)
	if err != nil {
		return nil, err
	}
	out := make(map[time.Time]HoldingsValue, len(days))
	for _, day := range days {
		v := HoldingsValue{ByAccount: map[uuid.UUID]int64{}}
		var buys []Buy
		for _, b := range h.buys {
			if !b.TradedOn.After(day) {
				buys = append(buys, b)
			}
		}
		var sales []LotSale
		for _, l := range h.sales {
			if !l.SoldOn.After(day) {
				sales = append(sales, l)
			}
		}
		lots, _ := OpenLots(buys, sales)
		held := map[positionKey]decimal.Decimal{}
		for _, lot := range lots {
			key := positionKey{lot.ISIN, lot.AccountID}
			held[key] = held[key].Add(lot.Quantity)
		}
		for key, quantity := range held {
			symbol := h.instruments[key.isin].YahooSymbol
			if symbol == nil {
				v.Unvalued++
				continue
			}
			q, ok := closes[*symbol][day]
			if !ok || !q.Currency.Valid() {
				v.Unvalued++
				continue
			}
			value := quantity.Mul(q.Close).Mul(minorPerUnit(q.Currency)).Round(0).IntPart()
			inCZK, ok := rates.ToCZK(value, q.Currency, day)
			if !ok {
				v.Unvalued++
				continue
			}
			v.ByAccount[key.account] += inCZK
		}
		out[day] = v
	}
	return out, nil
}

// closesOn is each symbol's last close on or before each day.
func (r *Repo) closesOn(ctx context.Context, symbols []string, days []time.Time) (map[string]map[time.Time]Quote, error) {
	out := map[string]map[time.Time]Quote{}
	if len(symbols) == 0 || len(days) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT s.symbol, d.day, b.day, b.close::text, s.currency
		FROM unnest($1::date[]) AS d (day) CROSS JOIN price_series s
		CROSS JOIN LATERAL (SELECT day, close FROM price_bar WHERE symbol = s.symbol AND day <= d.day ORDER BY day DESC LIMIT 1) b
		WHERE s.symbol = ANY($2) AND s.currency IS NOT NULL`, days, symbols)
	if err != nil {
		return nil, eris.Wrap(err, "closes on days")
	}
	defer rows.Close()
	for rows.Next() {
		var symbol, closePrice string
		var day time.Time
		var q Quote
		if err := rows.Scan(&symbol, &day, &q.Day, &closePrice, &q.Currency); err != nil {
			return nil, eris.Wrap(err, "read close on day")
		}
		if q.Close, err = decimal.NewFromString(closePrice); err != nil {
			return nil, eris.Wrap(err, "read close on day")
		}
		if out[symbol] == nil {
			out[symbol] = map[time.Time]Quote{}
		}
		out[symbol][day] = q
	}
	return out, eris.Wrap(rows.Err(), "read closes on days")
}
