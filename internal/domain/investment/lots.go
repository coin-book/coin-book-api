package investment

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/platform/money"
)

// TimeTestYears is how long a lot must be held for its gain to be exempt from Czech
// income tax (§ 4 odst. 1 písm. w) ZDP); the holding must exceed it.
const TimeTestYears = 3

// ExemptFrom is the first sale day on which a lot bought on boughtOn passes the time test.
// A period counted in years ends on the same day, or on the month's last day when the month
// is shorter (§ 605 OZ): bought on 29 Feb 2020, exempt from 1 Mar 2023, not 2 Mar as
// time.AddDate's normalisation would have it.
func ExemptFrom(boughtOn time.Time) time.Time {
	year, month := boughtOn.Year()+TimeTestYears, boughtOn.Month()
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, boughtOn.Location()).Day()
	end := time.Date(year, month, min(boughtOn.Day(), lastDay), 0, 0, 0, 0, boughtOn.Location())
	return end.AddDate(0, 0, 1)
}

func PassesTimeTest(boughtOn, soldOn time.Time) bool {
	return !soldOn.Before(ExemptFrom(boughtOn))
}

type Buy struct {
	AccountID   uuid.UUID
	ISIN        string
	TradedOn    time.Time
	Quantity    decimal.Decimal
	Currency    money.Currency
	NetMinor    int64
	NetCZKMinor *int64
}

type LotSale struct {
	AccountID        uuid.UUID
	ISIN             string
	BoughtOn         time.Time
	SoldOn           time.Time
	Quantity         decimal.Decimal
	Currency         money.Currency
	ProceedsMinor    int64
	CostMinor        int64
	GainMinor        int64
	ProceedsCZKMinor *int64
	CostCZKMinor     *int64
	GainCZKMinor     *int64
}

// Lot is what is left of one day's purchases after the broker's sold lots.
type Lot struct {
	AccountID uuid.UUID
	ISIN      string
	BoughtOn  time.Time
	Quantity  decimal.Decimal
	Currency  money.Currency
	CostMinor int64
	// CostCZKMinor is nil when a purchase on that day came without its CZK value.
	CostCZKMinor *int64
}

type lotKey struct {
	account  uuid.UUID
	isin     string
	boughtOn time.Time
}

type purchase struct {
	quantity    decimal.Decimal
	currency    money.Currency
	netMinor    int64
	netCZKMinor *int64
}

// OpenLots subtracts each sold lot from the purchases of its account, instrument and
// purchase day. A sale with no purchase to come from means the imported history
// starts after that purchase; its instrument is reported in incomplete.
func OpenLots(buys []Buy, sales []LotSale) (lots []Lot, incomplete map[string]bool) {
	purchases := map[lotKey]*purchase{}
	var order []lotKey
	for _, b := range buys {
		key := lotKey{b.AccountID, b.ISIN, b.TradedOn}
		p, ok := purchases[key]
		if !ok {
			p = &purchase{currency: b.Currency, netCZKMinor: new(int64)}
			purchases[key] = p
			order = append(order, key)
		}
		p.quantity = p.quantity.Add(b.Quantity)
		p.netMinor += b.NetMinor
		if b.NetCZKMinor == nil || p.netCZKMinor == nil {
			p.netCZKMinor = nil
		} else {
			*p.netCZKMinor += *b.NetCZKMinor
		}
	}
	sold := map[lotKey]decimal.Decimal{}
	incomplete = map[string]bool{}
	for _, s := range sales {
		key := lotKey{s.AccountID, s.ISIN, s.BoughtOn}
		sold[key] = sold[key].Add(s.Quantity)
		if p, ok := purchases[key]; !ok || sold[key].GreaterThan(p.quantity) {
			incomplete[s.ISIN] = true
		}
	}
	for _, key := range order {
		p := purchases[key]
		remaining := p.quantity.Sub(sold[key])
		if !remaining.IsPositive() {
			continue
		}
		lot := Lot{AccountID: key.account, ISIN: key.isin, BoughtOn: key.boughtOn, Quantity: remaining, Currency: p.currency,
			CostMinor: share(p.netMinor, remaining, p.quantity)}
		if p.netCZKMinor != nil {
			cost := share(*p.netCZKMinor, remaining, p.quantity)
			lot.CostCZKMinor = &cost
		}
		lots = append(lots, lot)
	}
	return lots, incomplete
}

func share(total int64, part, whole decimal.Decimal) int64 {
	return decimal.NewFromInt(total).Mul(part).Div(whole).Round(0).IntPart()
}
