package loan

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/db"
)

type Service struct {
	pool  *db.Pool
	repo  *Repo
	clock clock.Clock
}

func NewService(pool *db.Pool, repo *Repo, c clock.Clock) *Service {
	return &Service{pool: pool, repo: repo, clock: c}
}

// Overview is a loan as of today.
type Overview struct {
	Loan     Loan
	Terms    []Terms
	Schedule *Schedule
	// Current is the terms in force today; nil before the first terms.
	Current      *Terms
	BalanceMinor int64
	// Next is the next instalment due after today; nil once the loan is repaid.
	Next     *Instalment
	Payments []PaymentRecord
	// Years is the interest per calendar year, with what can be deducted from the tax base
	// for one's own housing; only a mortgage has a deduction.
	Years []InterestYear
	// Reserve is set for a mortgage with a reserve that interest is not charged on.
	Reserve *ReserveEffect
}

// ReserveEffect compares the loan with its reserve against the same loan without one.
type ReserveEffect struct {
	AccountID    uuid.UUID
	BalanceMinor int64
	// SavedMinor is the interest the reserve saved on the instalments due by today.
	SavedMinor int64
	// SavesMinor is what it will save on the projected ones if it stays as it is.
	SavesMinor int64
	// PaidOffOn and PaidOffWithoutOn are the last instalment's due day with and without it;
	// nil when the schedule has none.
	PaidOffOn        *time.Time
	PaidOffWithoutOn *time.Time
}

func (s *Service) Overview(ctx context.Context, accountID uuid.UUID) (*Overview, error) {
	l, err := s.repo.loan(ctx, s.pool, accountID)
	if err != nil {
		return nil, err
	}
	o := &Overview{Loan: *l}
	if o.Terms, err = s.repo.terms(ctx, s.pool, accountID); err != nil {
		return nil, err
	}
	if o.Payments, err = s.repo.payments(ctx, s.pool, accountID); err != nil {
		return nil, err
	}
	if o.Schedule, err = s.schedule(ctx, s.pool, accountID); err != nil {
		return nil, err
	}
	today := clock.Today(s.clock)
	o.BalanceMinor = o.Schedule.BalanceOn(today)
	for i := range o.Terms {
		if !o.Terms[i].ValidFrom.After(today) {
			o.Current = &o.Terms[i]
		}
	}
	for i := range o.Schedule.Instalments {
		if o.Schedule.Instalments[i].DueOn.After(today) {
			o.Next = &o.Schedule.Instalments[i]
			break
		}
	}
	o.Years = years(o, l.Kind)
	if o.Reserve, err = s.reserveEffect(ctx, accountID, o.Schedule, today); err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) reserveEffect(ctx context.Context, accountID uuid.UUID, with *Schedule, today time.Time) (*ReserveEffect, error) {
	reserve, err := s.repo.loanReserve(ctx, s.pool, accountID)
	if err != nil || reserve == nil {
		return nil, err
	}
	terms, err := s.repo.terms(ctx, s.pool, accountID)
	if err != nil {
		return nil, err
	}
	anchors, err := s.repo.anchors(ctx, s.pool, accountID)
	if err != nil {
		return nil, err
	}
	records, err := s.repo.payments(ctx, s.pool, accountID)
	if err != nil {
		return nil, err
	}
	payments := make([]Payment, len(records))
	for i, r := range records {
		payments[i] = r.Payment
	}
	without := Build(terms, anchors, payments, nil, today)
	e := &ReserveEffect{AccountID: reserve.AccountID, BalanceMinor: reserve.BalanceOn(today),
		PaidOffOn: lastDue(with), PaidOffWithoutOn: lastDue(without)}
	sum := func(s *Schedule, projected bool) int64 {
		var total int64
		for _, in := range s.Instalments {
			if in.Projected == projected {
				total += in.InterestMinor
			}
		}
		return total
	}
	e.SavedMinor = sum(without, false) - sum(with, false)
	e.SavesMinor = sum(without, true) - sum(with, true)
	return e, nil
}

func lastDue(s *Schedule) *time.Time {
	if len(s.Instalments) == 0 {
		return nil
	}
	return &s.Instalments[len(s.Instalments)-1].DueOn
}

func years(o *Overview, kind account.Kind) []InterestYear {
	byYear := o.Schedule.InterestByYear()
	out := make([]InterestYear, 0, len(byYear))
	for year, interest := range byYear {
		y := InterestYear{Year: year, InterestMinor: interest}
		if kind == account.Mortgage && len(o.Terms) > 0 {
			y.DeductibleMinor = min(interest, DeductionLimit(o.Terms[0].ValidFrom))
		}
		out = append(out, y)
	}
	slices.SortFunc(out, func(a, b InterestYear) int { return b.Year - a.Year })
	return out
}

func (s *Service) schedule(ctx context.Context, q db.Querier, accountID uuid.UUID) (*Schedule, error) {
	terms, err := s.repo.terms(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	anchors, err := s.repo.anchors(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	records, err := s.repo.payments(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	payments := make([]Payment, len(records))
	for i, r := range records {
		payments[i] = r.Payment
	}
	reserve, err := s.repo.loanReserve(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	if reserve == nil {
		return Build(terms, anchors, payments, nil, clock.Today(s.clock)), nil
	}
	return Build(terms, anchors, payments, reserve, clock.Today(s.clock)), nil
}

// Schedules are every loan's schedule, for valuing them on any day.
func (s *Service) Schedules(ctx context.Context) (map[uuid.UUID]*Schedule, error) {
	loans, err := s.repo.loans(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*Schedule, len(loans))
	for _, l := range loans {
		if out[l.AccountID], err = s.schedule(ctx, s.pool, l.AccountID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Service) SaveTerms(ctx context.Context, accountID uuid.UUID, t Terms) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.repo.loan(ctx, tx, accountID); err != nil {
			return err
		}
		if err := s.repo.saveTerms(ctx, tx, accountID, t); err != nil {
			return err
		}
		return s.Reconcile(ctx, tx)
	})
}

func (s *Service) DeleteTerms(ctx context.Context, accountID uuid.UUID, validFrom time.Time) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.repo.loan(ctx, tx, accountID); err != nil {
			return err
		}
		if err := s.repo.deleteTerms(ctx, tx, accountID, validFrom); err != nil {
			return err
		}
		return s.Reconcile(ctx, tx)
	})
}

// Prepayment simulates an extra payment today on the terms in force and the balance owed.
func (s *Service) Prepayment(ctx context.Context, accountID uuid.UUID, extra int64) (*Prepayment, error) {
	o, err := s.Overview(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if o.Current == nil || o.BalanceMinor <= 0 {
		return nil, ErrTermsNotFound
	}
	if extra >= o.BalanceMinor {
		return nil, ErrExtraTooLarge
	}
	today := clock.Today(s.clock)
	var held int64
	if o.Reserve != nil {
		held = o.Reserve.BalanceMinor
	}
	p := SimulatePrepayment(*o.Current, o.BalanceMinor, held, extra, today)
	return &p, nil
}

// MarkPayment records a transaction as paying the loan; a nil split follows the schedule.
func (s *Service) MarkPayment(ctx context.Context, transactionID string, accountID uuid.UUID, fixed *Split) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := s.repo.loan(ctx, tx, accountID)
		if err != nil {
			return err
		}
		t, err := s.repo.transaction(ctx, tx, transactionID)
		if err != nil {
			return err
		}
		if t.amount >= 0 || t.paired || t.accountID == accountID || t.currency != l.Currency {
			return ErrNotAPayment
		}
		if fixed != nil && fixed.InterestMinor+fixed.PrincipalMinor != -t.amount {
			return ErrSplitMismatch
		}
		if _, err := s.repo.markPayment(ctx, tx, transactionID, accountID, fixed); err != nil {
			return err
		}
		return s.Reconcile(ctx, tx)
	})
}

func (s *Service) UnmarkPayment(ctx context.Context, transactionID string) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.repo.unmarkPayment(ctx, tx, transactionID); err != nil {
			return err
		}
		return s.Reconcile(ctx, tx)
	})
}

// Reconcile adopts one-leg transfers to loans as payments and splits every loan's payments
// by its schedule again. It runs after anything that changes transfers, terms or balances.
func (s *Service) Reconcile(ctx context.Context, q db.Querier) error {
	if err := s.repo.adoptOwnTransfers(ctx, q); err != nil {
		return err
	}
	loans, err := s.repo.loans(ctx, q)
	if err != nil {
		return err
	}
	for _, l := range loans {
		schedule, err := s.schedule(ctx, q, l.AccountID)
		if err != nil {
			return err
		}
		if err := s.repo.writeSplits(ctx, q, l, schedule.Splits); err != nil {
			return err
		}
	}
	return nil
}
