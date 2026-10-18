package investment

import (
	"context"
	"slices"
)

// YearGains is one calendar year of sold lots and dividends, in CZK as the statements
// convert them. Lots without CZK values are counted in MissingCZK and left out of the sums.
type YearGains struct {
	Year             int
	Lots             int
	MissingCZK       int
	ProceedsCZKMinor int64
	CostCZKMinor     int64
	GainCZKMinor     int64
	// ExemptGainCZKMinor is from lots that passed the time test.
	ExemptGainCZKMinor  int64
	TaxableGainCZKMinor int64
	// TaxableProceedsCZKMinor is what the yearly 100 000 Kč exemption of § 4 odst. 1 písm. w) ZDP is tested on.
	TaxableProceedsCZKMinor int64
	DividendsGrossCZKMinor  int64
	DividendsTaxCZKMinor    int64
	DividendsNetCZKMinor    int64
}

type SoldLot struct {
	LotSale
	Instrument Instrument
	Exempt     bool
}

type Realized struct {
	// Years are newest first.
	Years []YearGains
	// Lots are newest sale first.
	Lots []SoldLot
}

func (s *Service) Realized(ctx context.Context) (*Realized, error) {
	h, err := s.repo.history(ctx, nil)
	if err != nil {
		return nil, err
	}
	return realize(h), nil
}

func realize(h *history) *Realized {
	years := map[int]*YearGains{}
	year := func(y int) *YearGains {
		if g, ok := years[y]; ok {
			return g
		}
		g := &YearGains{Year: y}
		years[y] = g
		return g
	}
	r := &Realized{Lots: make([]SoldLot, 0, len(h.sales))}
	for _, sale := range h.sales {
		exempt := PassesTimeTest(sale.BoughtOn, sale.SoldOn)
		r.Lots = append(r.Lots, SoldLot{LotSale: sale, Instrument: h.instruments[sale.ISIN], Exempt: exempt})
		g := year(sale.SoldOn.Year())
		g.Lots++
		if sale.ProceedsCZKMinor == nil || sale.CostCZKMinor == nil || sale.GainCZKMinor == nil {
			g.MissingCZK++
			continue
		}
		g.ProceedsCZKMinor += *sale.ProceedsCZKMinor
		g.CostCZKMinor += *sale.CostCZKMinor
		g.GainCZKMinor += *sale.GainCZKMinor
		if exempt {
			g.ExemptGainCZKMinor += *sale.GainCZKMinor
		} else {
			g.TaxableGainCZKMinor += *sale.GainCZKMinor
			g.TaxableProceedsCZKMinor += *sale.ProceedsCZKMinor
		}
	}
	for _, d := range h.dividends {
		g := year(d.PaidOn.Year())
		if d.GrossCZKMinor != nil {
			g.DividendsGrossCZKMinor += *d.GrossCZKMinor
		}
		if d.NetCZKMinor != nil {
			g.DividendsNetCZKMinor += *d.NetCZKMinor
		}
		g.DividendsTaxCZKMinor += d.TaxCZKMinor
	}
	for _, g := range years {
		r.Years = append(r.Years, *g)
	}
	slices.SortFunc(r.Years, func(a, b YearGains) int { return b.Year - a.Year })
	slices.SortStableFunc(r.Lots, func(a, b SoldLot) int { return b.SoldOn.Compare(a.SoldOn) })
	return r
}
