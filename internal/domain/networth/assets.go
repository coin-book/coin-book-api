package networth

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

// healthyRepayment is how Fingood labels a loan repaid on time; any other label, a delay or
// an overdue amount puts the outstanding principal at risk.
const healthyRepayment = "V pořádku"

func (s *Service) AccountCurrency(ctx context.Context, accountID uuid.UUID) (money.Currency, error) {
	a, err := s.repo.account(ctx, s.repo.pool, accountID)
	if err != nil {
		return "", err
	}
	return a.Currency, nil
}

func (s *Service) Valuations(ctx context.Context, accountID uuid.UUID) ([]Valuation, error) {
	if _, err := s.valuedByHand(ctx, s.repo.pool, accountID); err != nil {
		return nil, err
	}
	return s.repo.valuations(ctx, accountID)
}

// SaveValuation records an asset's value, or the balance owed on a loan, at the end of a day;
// a second one for the same day replaces the first.
func (s *Service) SaveValuation(ctx context.Context, v *Valuation) error {
	return db.WithTx(ctx, s.repo.pool, func(tx pgx.Tx) error {
		a, err := s.valuedByHand(ctx, tx, v.AccountID)
		if err != nil {
			return err
		}
		if err := s.repo.saveValuation(ctx, tx, v); err != nil {
			return err
		}
		if a.Kind.IsLiability() || a.Kind == account.LoanReserve {
			return s.loans.Reconcile(ctx, tx)
		}
		return nil
	})
}

func (s *Service) DeleteValuation(ctx context.Context, accountID, id uuid.UUID) error {
	return db.WithTx(ctx, s.repo.pool, func(tx pgx.Tx) error {
		a, err := s.valuedByHand(ctx, tx, accountID)
		if err != nil {
			return err
		}
		if err := s.repo.deleteValuation(ctx, tx, accountID, id); err != nil {
			return err
		}
		if a.Kind.IsLiability() || a.Kind == account.LoanReserve {
			return s.loans.Reconcile(ctx, tx)
		}
		return nil
	})
}

func (s *Service) valuedByHand(ctx context.Context, q db.Querier, accountID uuid.UUID) (*account.Account, error) {
	a, err := s.repo.account(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	switch a.Kind.Valuation() {
	case account.BySnapshot, account.BySchedule, account.ByFlows:
		return a, nil
	default:
		return nil, ErrNotValuedByHand
	}
}

// Performance is what an asset valued at dates earned over the money put into it.
type Performance struct {
	ContributedMinor int64
	WithdrawnMinor   int64
	// ValueMinor is the latest value, on ValuedOn; nil before the first.
	ValueMinor *int64
	ValuedOn   *time.Time
	// GainMinor is the value less the net contributions; nil without a value.
	GainMinor *int64
	// AnnualReturn is the money-weighted return (XIRR); nil when it cannot be told.
	AnnualReturn *decimal.Decimal
	Flows        []Flow
}

func (s *Service) Performance(ctx context.Context, accountID uuid.UUID) (*Performance, error) {
	a, err := s.repo.account(ctx, s.repo.pool, accountID)
	if err != nil {
		return nil, err
	}
	if a.Kind.Valuation() != account.BySnapshot {
		return nil, ErrNotValuedByHand
	}
	flows, err := s.repo.flows(ctx, accountID, a.Currency)
	if err != nil {
		return nil, err
	}
	dated, err := s.repo.datedValues(ctx)
	if err != nil {
		return nil, err
	}
	p := &Performance{Flows: flows}
	current, valued := latest(dated[accountID], time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC))
	// Money moved after the latest value is not in it yet; it counts once the next value is known.
	var held []cashFlow
	for _, f := range flows {
		if valued && f.On.After(current.on) {
			continue
		}
		if f.AmountMinor > 0 {
			p.ContributedMinor += f.AmountMinor
		} else {
			p.WithdrawnMinor -= f.AmountMinor
		}
		held = append(held, cashFlow{f.On, -float64(f.AmountMinor)})
	}
	if !valued {
		return p, nil
	}
	gain := current.value - (p.ContributedMinor - p.WithdrawnMinor)
	p.ValueMinor, p.ValuedOn, p.GainMinor = &current.value, &current.on, &gain
	if rate, ok := xirr(append(held, cashFlow{current.on, float64(current.value)})); ok {
		r := decimal.NewFromFloat(rate).Round(4)
		p.AnnualReturn = &r
	}
	return p, nil
}

type P2PPosition struct {
	LoanNumber             string
	Name                   string
	Repayment              string
	Status                 string
	RepaymentStatus        string
	InvestedMinor          int64
	OutstandingMinor       int64
	InterestRate           decimal.Decimal
	InvestedOn             *time.Time
	NextPaymentOn          *time.Time
	NextPaymentMinor       *int64
	FinalPaymentOn         *time.Time
	DelayDays              int
	OverdueMinor           int64
	InterestPaidMinor      int64
	PenaltyInterestMinor   int64
	PrincipalReturnedMinor int64
	MarketGainMinor        int64
	FeesMinor              int64
}

func (p P2PPosition) AtRisk() bool {
	return p.OutstandingMinor > 0 && (p.DelayDays > 0 || p.OverdueMinor > 0 ||
		(p.RepaymentStatus != "" && p.RepaymentStatus != healthyRepayment))
}

// earned is the interest, penalties and market gains less fees, since investing.
func (p P2PPosition) earned() int64 {
	return p.InterestPaidMinor + p.PenaltyInterestMinor + p.MarketGainMinor - p.FeesMinor
}

type Maturity struct {
	// Month is nil for loans without a final payment date.
	Month            *time.Time
	OutstandingMinor int64
}

type SnapshotTotal struct {
	On               time.Time
	OutstandingMinor int64
	EarnedMinor      int64
}

// P2PSnapshot is the latest imported export of a platform's loans.
type P2PSnapshot struct {
	On               time.Time
	Currency         money.Currency
	Positions        []P2PPosition
	OutstandingMinor int64
	AtRiskMinor      int64
	// EarnedMinor is interest, penalties and market gains less fees since investing.
	EarnedMinor int64
	// EarnedSincePreviousMinor is nil for the first snapshot.
	EarnedSincePreviousMinor *int64
	PreviousOn               *time.Time
	// AverageRate is weighted by the outstanding principal.
	AverageRate decimal.Decimal
	// Maturities is the outstanding principal by the month of the loans' final payment.
	Maturities []Maturity
	History    []SnapshotTotal
}

func (s *Service) P2P(ctx context.Context, accountID uuid.UUID) (*P2PSnapshot, error) {
	a, err := s.repo.account(ctx, s.repo.pool, accountID)
	if err != nil {
		return nil, err
	}
	if a.Kind != account.P2P {
		return nil, ErrNoP2PSnapshot
	}
	history, err := s.repo.snapshotTotals(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return nil, ErrNoP2PSnapshot
	}
	last := history[len(history)-1]
	positions, err := s.repo.positions(ctx, accountID, last.On)
	if err != nil {
		return nil, err
	}
	snap := &P2PSnapshot{On: last.On, Currency: a.Currency, Positions: positions, History: history}
	weighted := decimal.Zero
	maturities := map[time.Time]int64{}
	var undated int64
	for _, p := range positions {
		snap.OutstandingMinor += p.OutstandingMinor
		snap.EarnedMinor += p.earned()
		if p.AtRisk() {
			snap.AtRiskMinor += p.OutstandingMinor
		}
		weighted = weighted.Add(p.InterestRate.Mul(decimal.NewFromInt(p.OutstandingMinor)))
		if p.OutstandingMinor == 0 {
			continue
		}
		if p.FinalPaymentOn == nil {
			undated += p.OutstandingMinor
			continue
		}
		month := time.Date(p.FinalPaymentOn.Year(), p.FinalPaymentOn.Month(), 1, 0, 0, 0, 0, time.UTC)
		maturities[month] += p.OutstandingMinor
	}
	if snap.OutstandingMinor > 0 {
		snap.AverageRate = weighted.Div(decimal.NewFromInt(snap.OutstandingMinor)).Round(2)
	}
	months := make([]time.Time, 0, len(maturities))
	for m := range maturities {
		months = append(months, m)
	}
	slices.SortFunc(months, func(a, b time.Time) int { return a.Compare(b) })
	for _, m := range months {
		snap.Maturities = append(snap.Maturities, Maturity{Month: &m, OutstandingMinor: maturities[m]})
	}
	if undated > 0 {
		snap.Maturities = append(snap.Maturities, Maturity{OutstandingMinor: undated})
	}
	if len(history) > 1 {
		previous := history[len(history)-2]
		since := last.EarnedMinor - previous.EarnedMinor
		snap.EarnedSincePreviousMinor, snap.PreviousOn = &since, &previous.On
	}
	return snap, nil
}

func (r *Repo) snapshotTotals(ctx context.Context, accountID uuid.UUID) ([]SnapshotTotal, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT snapshot_on, sum(outstanding_minor)::bigint,
			sum(interest_paid_minor + penalty_interest_minor + market_gain_minor - fees_minor)::bigint
		FROM p2p_position WHERE account_id = $1 GROUP BY snapshot_on ORDER BY snapshot_on`, accountID)
	if err != nil {
		return nil, eris.Wrap(err, "list P2P snapshots")
	}
	totals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SnapshotTotal, error) {
		var t SnapshotTotal
		err := row.Scan(&t.On, &t.OutstandingMinor, &t.EarnedMinor)
		return t, err
	})
	return totals, eris.Wrap(err, "read P2P snapshots")
}

func (r *Repo) positions(ctx context.Context, accountID uuid.UUID, on time.Time) ([]P2PPosition, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT loan_number, name, repayment, status, repayment_status, invested_minor, outstanding_minor, interest_rate::text,
			invested_on, next_payment_on, next_payment_minor, final_payment_on, delay_days, overdue_minor, interest_paid_minor,
			penalty_interest_minor, principal_returned_minor, market_gain_minor, fees_minor
		FROM p2p_position WHERE account_id = $1 AND snapshot_on = $2
		ORDER BY outstanding_minor DESC, final_payment_on NULLS LAST, loan_number`, accountID, on)
	if err != nil {
		return nil, eris.Wrap(err, "list P2P loans")
	}
	positions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (P2PPosition, error) {
		var p P2PPosition
		var rate string
		if err := row.Scan(&p.LoanNumber, &p.Name, &p.Repayment, &p.Status, &p.RepaymentStatus, &p.InvestedMinor,
			&p.OutstandingMinor, &rate, &p.InvestedOn, &p.NextPaymentOn, &p.NextPaymentMinor, &p.FinalPaymentOn, &p.DelayDays,
			&p.OverdueMinor, &p.InterestPaidMinor, &p.PenaltyInterestMinor, &p.PrincipalReturnedMinor, &p.MarketGainMinor,
			&p.FeesMinor); err != nil {
			return p, err
		}
		p.InterestRate, err = decimal.NewFromString(rate)
		return p, err
	})
	return positions, eris.Wrap(err, "read P2P loans")
}
