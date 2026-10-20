// Package loan values mortgages and loans by their repayment schedule and splits the
// bank payments that pay them into interest and principal (ADR 211).
package loan

import (
	"cmp"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// settledWindow is how late the bank may book an instalment that a balance stated after its due
// day already includes.
const settledWindow = 5 * 24 * time.Hour

// maxVisits bounds the walk past the longest loan a bank offers (fifty years), with room for the
// months a refixation visits twice.
const maxVisits = 1200

var monthsPerYear = decimal.NewFromInt(12)

// Terms hold from ValidFrom on: the balance owed that day, the annual rate in percent
// and the monthly instalment due on PaymentDay (the month's last day when shorter).
type Terms struct {
	ValidFrom    time.Time
	BalanceMinor int64
	AnnualRate   decimal.Decimal
	PaymentMinor int64
	PaymentDay   int
	MaturityOn   time.Time
	// FixedUntil is nil for a rate that is not fixed.
	FixedUntil *time.Time
}

// Anchor is a balance the user read off a bank statement; the schedule continues from it.
type Anchor struct {
	On           time.Time
	BalanceMinor int64
}

type Split struct {
	InterestMinor  int64
	PrincipalMinor int64
}

// Payment is a bank transaction paying the loan; AmountMinor is positive.
type Payment struct {
	TransactionID string
	BookedOn      time.Time
	AmountMinor   int64
	// Fixed is the user's own split; nil follows the schedule.
	Fixed *Split
}

type Instalment struct {
	DueOn          time.Time
	InterestMinor  int64
	PrincipalMinor int64
	// BalanceMinor is what is owed after this instalment.
	BalanceMinor int64
	// Paid: bank payments booked nearest its due day paid it. Otherwise an instalment
	// due by today is assumed paid as scheduled, and a later one is Projected.
	Paid      bool
	Projected bool
}

type balancePoint struct {
	on      time.Time
	balance int64
}

type Schedule struct {
	Instalments []Instalment
	// Splits are the interest and principal of each payment, by transaction id.
	Splits map[string]Split
	points []balancePoint
}

// BalanceOn is what is owed at the end of the day; zero before the loan's first terms.
func (s *Schedule) BalanceOn(day time.Time) int64 {
	var balance int64
	for _, p := range s.points {
		if p.on.After(day) {
			break
		}
		balance = p.balance
	}
	return balance
}

// InterestByYear sums the interest of instalments due by today, per calendar year.
func (s *Schedule) InterestByYear() map[int]int64 {
	out := map[int]int64{}
	for _, in := range s.Instalments {
		if !in.Projected {
			out[in.DueOn.Year()] += in.InterestMinor
		}
	}
	return out
}

// Offset is money held against the loan that interest is not charged on, such as Air Bank's
// Chytrá rezerva: what it held at the end of a day.
type Offset interface {
	BalanceOn(day time.Time) int64
}

// visit is a due day the schedule walks: under which terms, and whether the terms' stated
// balance already includes it (a due day on or before the terms' first day).
type visit struct {
	due     time.Time
	terms   int
	settled bool
}

// visits are the due days in order. Terms take over when they start before a due day, or, when
// their payment day is later than the old one, later in the month the old instalment was due.
func visits(terms []Terms) []visit {
	var out []visit
	current := 0
	month := monthOf(terms[0].ValidFrom)
	last := terms[len(terms)-1].MaturityOn
	var lastDue time.Time
	for range maxVisits {
		due := dueDay(month, terms[current].PaymentDay)
		for current+1 < len(terms) {
			next := terms[current+1]
			nextDue := dueDay(month, next.PaymentDay)
			if !next.ValidFrom.Before(due) && (due.After(lastDue) || !next.ValidFrom.Before(nextDue)) {
				break
			}
			current++
			due = nextDue
		}
		if due.After(lastDue) {
			out = append(out, visit{due: due, terms: current, settled: !due.After(terms[current].ValidFrom)})
			lastDue = due
		}
		// The first due day on or after maturity takes what is left.
		if !due.Before(last) && current == len(terms)-1 {
			break
		}
		if current+1 < len(terms) {
			if nextDue := dueDay(month, terms[current+1].PaymentDay); nextDue.After(due) && terms[current+1].ValidFrom.Before(nextDue) {
				continue
			}
		}
		month = month.AddDate(0, 1, 0)
	}
	return out
}

// assign gives each payment to the visited due day nearest its booking (the earlier on a tie), so
// an instalment the bank books a few days late (a weekend, a holiday) still pays its own.
func assign(payments []Payment, vs []visit) map[int][]Payment {
	out := map[int][]Payment{}
	if len(vs) == 0 {
		return out
	}
	for _, p := range payments {
		i, _ := slices.BinarySearchFunc(vs, p.BookedOn, func(v visit, day time.Time) int { return v.due.Compare(day) })
		best := min(i, len(vs)-1)
		if i > 0 && (i == len(vs) || absDays(p.BookedOn, vs[i-1].due) <= absDays(p.BookedOn, vs[i].due)) {
			best = i - 1
		}
		out[best] = append(out[best], p)
	}
	for _, list := range out {
		slices.SortStableFunc(list, func(a, b Payment) int { return a.BookedOn.Compare(b.BookedOn) })
	}
	return out
}

// Build walks the loan from its first terms, due day by due day. A stated balance (the terms' own
// or one the user read off a statement) is what was owed at the end of its day: it includes every
// payment booked by then, and later payments lower it on the day they were booked. Payments booked
// nearest a due day pay that instalment, the nearest of them carrying its interest; an instalment
// due by today without payments counts as paid as scheduled, later ones are projected until the
// balance is repaid; whatever is left at maturity is repaid then. A nil offset charges interest on
// the whole balance.
func Build(terms []Terms, anchors []Anchor, payments []Payment, offset Offset, today time.Time) *Schedule {
	s := &Schedule{Splits: map[string]Split{}}
	if len(terms) == 0 {
		return s
	}
	terms = slices.Clone(terms)
	slices.SortFunc(terms, func(a, b Terms) int { return a.ValidFrom.Compare(b.ValidFrom) })
	anchors = slices.Clone(anchors)
	slices.SortFunc(anchors, func(a, b Anchor) int { return a.On.Compare(b.On) })
	vs := visits(terms)
	paidAt := assign(payments, vs)
	lastPaid := -1
	for i := range paidAt {
		lastPaid = max(lastPaid, i)
	}

	w := &walk{s: s}
	current, nextAnchor := -1, 0
	for i, v := range vs {
		if v.terms != current {
			current = v.terms
			w.catchUp(terms[current].ValidFrom, anchors, &nextAnchor)
			// Terms entered for after the loan was repaid do not bring the debt back.
			if !w.repaid {
				w.state(terms[current].ValidFrom, terms[current].BalanceMinor)
			}
		}
		t := terms[current]
		w.catchUp(v.due, anchors, &nextAnchor)
		paid := paidAt[i]
		// Repaid: nothing more is due, whatever terms were entered for later.
		if w.balance <= 0 && len(paid) == 0 && len(w.pending) == 0 {
			if i > lastPaid {
				break
			}
			continue
		}
		var held int64
		if offset != nil {
			held = offset.BalanceOn(v.due.AddDate(0, 0, -1))
		}
		interest := monthlyInterest(max(0, w.balance-held), t.AnnualRate)
		final := !v.due.Before(t.MaturityOn) && i >= lastPaid && current == len(terms)-1
		in := Instalment{DueOn: v.due, Paid: len(paid) > 0}
		var deferred int64
		switch {
		case len(paid) > 0:
			included := w.included(paid, v, t)
			if v.settled && included == "" {
				// No regular instalment here: what was paid is extra and repays principal only.
				interest = 0
			}
			in.InterestMinor, in.PrincipalMinor = s.split(paid, interest, v.due)
			deferred = w.repay(paid, v, included)
		case !v.settled:
			in.Projected = v.due.After(today)
			in.InterestMinor = interest
			in.PrincipalMinor = max(0, min(t.PaymentMinor-interest, w.balance))
			if final {
				in.PrincipalMinor = w.balance
			}
			w.lower(v.due, in.PrincipalMinor)
		}
		if v.settled {
			continue
		}
		if final {
			// What 30/360 interest leaves over against the bank's own figures is gone at maturity.
			w.catchUp(v.due, nil, nil)
			w.balance = 0
		}
		// The instalment's row shows the balance after it, even when the bank booked it a few days late;
		// the balance itself drops on the booking day.
		in.BalanceMinor = max(0, w.balance-deferred)
		s.Instalments = append(s.Instalments, in)
		s.points = append(s.points, balancePoint{v.due, w.balance})
		w.repaid = w.repaid || in.BalanceMinor == 0
	}
	w.catchUp(time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), anchors, &nextAnchor)
	return s
}

// reduction is principal paid on a day that lowers the balance once that day is reached.
type reduction struct {
	on        time.Time
	principal int64
}

// walk keeps the balance as stated balances and payments arrive in the order of their days.
type walk struct {
	s        *Schedule
	balance  int64
	statedOn time.Time
	// pending are reductions booked after their instalment's due day, applied on their own day.
	pending []reduction
	repaid  bool
}

func (w *walk) state(on time.Time, value int64) {
	w.balance, w.statedOn = value, on
	// A stated balance already includes what was paid by its day.
	w.pending = slices.DeleteFunc(w.pending, func(r reduction) bool { return !r.on.After(on) })
	w.s.points = append(w.s.points, balancePoint{on, value})
}

func (w *walk) lower(on time.Time, principal int64) {
	w.balance = max(0, w.balance-principal)
	w.s.points = append(w.s.points, balancePoint{on, w.balance})
}

// catchUp applies, in the order of their days, the statements and late payments before day.
func (w *walk) catchUp(day time.Time, anchors []Anchor, next *int) {
	for {
		var anchor *Anchor
		if next != nil && *next < len(anchors) && anchors[*next].On.Before(day) {
			anchor = &anchors[*next]
		}
		var late *reduction
		if len(w.pending) > 0 && w.pending[0].on.Before(day) {
			late = &w.pending[0]
		}
		switch {
		case anchor != nil && (late == nil || !late.on.Before(anchor.On)):
			*next++
			if !anchor.On.Before(w.statedOn) {
				w.state(anchor.On, anchor.BalanceMinor)
			}
		case late != nil:
			w.pending = w.pending[1:]
			w.lower(late.on, late.principal)
		default:
			return
		}
	}
}

// included is the payment of an instalment the terms' balance already includes; "" for none.
func (w *walk) included(paid []Payment, v visit, t Terms) string {
	if !v.settled {
		return ""
	}
	n := nearest(paid, v.due)
	if !n.BookedOn.After(w.statedOn) {
		return n.TransactionID
	}
	// Booked a few days late, a regular instalment is still in the balance stated after its due day;
	// a payment much larger than an instalment is an extra one made after that.
	if absDays(n.BookedOn, v.due) <= settledWindow && n.AmountMinor <= regularCeiling(t) {
		return n.TransactionID
	}
	return ""
}

// regularCeiling is the most a payment may be to pass for the instalment it is nearest to.
func regularCeiling(t Terms) int64 { return t.PaymentMinor * 3 / 2 }

// repay lowers the balance by the principal of payments booked after it was stated, other than the
// included one. One booked by the due day lowers it on its own day; one booked late, on its day
// once that is reached.
// It returns the principal left for the late payments' own days.
func (w *walk) repay(paid []Payment, v visit, included string) (deferred int64) {
	for _, p := range paid {
		if !p.BookedOn.After(w.statedOn) || p.TransactionID == included {
			continue
		}
		principal := w.s.Splits[p.TransactionID].PrincipalMinor
		if p.BookedOn.After(v.due) {
			w.pending = append(w.pending, reduction{p.BookedOn, principal})
			slices.SortStableFunc(w.pending, func(a, b reduction) int { return a.on.Compare(b.on) })
			deferred += principal
			continue
		}
		w.lower(p.BookedOn, principal)
	}
	return deferred
}

// split gives the instalment's interest to the payment booked nearest its due day, then to the
// others; a split the user entered stays as it is.
func (s *Schedule) split(paid []Payment, interest int64, due time.Time) (interestPaid, principalPaid int64) {
	for _, p := range paid {
		if p.Fixed != nil {
			interest -= p.Fixed.InterestMinor
		}
	}
	interest = max(0, interest)
	for _, p := range byDistance(paid, due) {
		split := splitOrPrincipal(p)
		if p.Fixed == nil {
			split.InterestMinor = min(interest, p.AmountMinor)
			split.PrincipalMinor = p.AmountMinor - split.InterestMinor
			interest -= split.InterestMinor
		}
		s.Splits[p.TransactionID] = split
		interestPaid += split.InterestMinor
		principalPaid += split.PrincipalMinor
	}
	return interestPaid, principalPaid
}

// byDistance orders payments by how near their booking is to the due day, the earlier first on a tie.
func byDistance(paid []Payment, due time.Time) []Payment {
	out := slices.Clone(paid)
	slices.SortStableFunc(out, func(a, b Payment) int { return cmp.Compare(absDays(a.BookedOn, due), absDays(b.BookedOn, due)) })
	return out
}

func nearest(paid []Payment, due time.Time) Payment { return byDistance(paid, due)[0] }

func splitOrPrincipal(p Payment) Split {
	if p.Fixed != nil {
		return *p.Fixed
	}
	return Split{PrincipalMinor: p.AmountMinor}
}

// monthlyInterest charges a twelfth of the annual rate on the balance (30/360).
func monthlyInterest(balance int64, annualRate decimal.Decimal) int64 {
	return decimal.NewFromInt(balance).Mul(annualRate).Div(decimal.NewFromInt(100)).Div(monthsPerYear).Round(0).IntPart()
}

func absDays(a, b time.Time) time.Duration {
	if a.After(b) {
		return a.Sub(b)
	}
	return b.Sub(a)
}

func monthOf(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// dueDay is the month's paymentDay, or its last day when the month is shorter.
func dueDay(month time.Time, paymentDay int) time.Time {
	last := month.AddDate(0, 1, -1).Day()
	return time.Date(month.Year(), month.Month(), min(paymentDay, last), 0, 0, 0, 0, time.UTC)
}
