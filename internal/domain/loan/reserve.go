package loan

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/platform/db"
)

var ErrReserveExists = eris.New("loan: the mortgage already has a reserve")

// reserveMove is money that went into the reserve (positive) or was taken out of it.
type reserveMove struct {
	on     time.Time
	amount int64
}

// Reserve is what a loan's reserve held over time: statement balances the user entered, moved by
// the transfers into and out of it since.
type Reserve struct {
	AccountID uuid.UUID
	anchors   []Anchor
	moves     []reserveMove
}

// BalanceOn is the reserve at the end of a day, never below zero.
func (r *Reserve) BalanceOn(day time.Time) int64 {
	if r == nil {
		return 0
	}
	var balance int64
	from := time.Time{}
	for _, a := range r.anchors {
		if a.On.After(day) {
			break
		}
		balance, from = a.BalanceMinor, a.On
	}
	for _, m := range r.moves {
		if m.on.After(day) {
			break
		}
		if m.on.After(from) {
			balance += m.amount
		}
	}
	return max(0, balance)
}

func (r *Repo) reserveAccount(ctx context.Context, q db.Querier, loanID uuid.UUID) (*uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM account WHERE parent_id = $1 AND kind = 'loan_reserve' ORDER BY created_at LIMIT 1`,
		loanID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &id, eris.Wrap(err, "find loan reserve")
}

func (r *Repo) reserve(ctx context.Context, q db.Querier, reserveID uuid.UUID) (*Reserve, error) {
	anchors, err := r.anchors(ctx, q, reserveID)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `
		SELECT t.booked_on, -t.amount_minor FROM transfer_leg tl JOIN transaction t ON t.id = tl.transaction_id
		JOIN account a ON a.id = $1
		WHERE tl.counterpart_account_id = $1 AND t.account_id <> $1 AND t.currency = a.currency
		ORDER BY t.booked_on`, reserveID)
	if err != nil {
		return nil, eris.Wrap(err, "list reserve transfers")
	}
	moves, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (reserveMove, error) {
		var m reserveMove
		err := row.Scan(&m.on, &m.amount)
		return m, err
	})
	if err != nil {
		return nil, eris.Wrap(err, "read reserve transfers")
	}
	slices.SortStableFunc(moves, func(a, b reserveMove) int { return a.on.Compare(b.on) })
	return &Reserve{AccountID: reserveID, anchors: anchors, moves: moves}, nil
}

// loanReserve is the loan's reserve; nil when the loan has none.
func (r *Repo) loanReserve(ctx context.Context, q db.Querier, loanID uuid.UUID) (*Reserve, error) {
	id, err := r.reserveAccount(ctx, q, loanID)
	if err != nil || id == nil {
		return nil, err
	}
	return r.reserve(ctx, q, *id)
}

// Reserves are every loan reserve's history, by the reserve's account id.
func (s *Service) Reserves(ctx context.Context) (map[uuid.UUID]*Reserve, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM account WHERE kind = 'loan_reserve'`)
	if err != nil {
		return nil, eris.Wrap(err, "list loan reserves")
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, eris.Wrap(err, "read loan reserves")
	}
	out := make(map[uuid.UUID]*Reserve, len(ids))
	for _, id := range ids {
		if out[id], err = s.repo.reserve(ctx, s.pool, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EnableReserve gives a mortgage an account for its reserve, at the same bank, so the bank's
// transfers into and out of the reserve are recognised (transfer.DetectOwn) and lower its interest.
func (s *Service) EnableReserve(ctx context.Context, q db.Querier, loanID uuid.UUID) (uuid.UUID, error) {
	l, err := s.repo.loan(ctx, q, loanID)
	if err != nil {
		return uuid.Nil, err
	}
	if l.Kind != account.Mortgage {
		return uuid.Nil, ErrNotALoan
	}
	existing, err := s.repo.reserveAccount(ctx, q, loanID)
	if err != nil {
		return uuid.Nil, err
	}
	if existing != nil {
		return *existing, ErrReserveExists
	}
	var id uuid.UUID
	err = q.QueryRow(ctx, `
		INSERT INTO account (name, institution, kind, currency, external_ref, parent_id, bank_id)
		SELECT name || ' – rezerva', institution, 'loan_reserve', currency, $2, id, bank_id FROM account WHERE id = $1
		RETURNING id`, loanID, account.ManualRef()).Scan(&id)
	if db.IsUniqueViolation(err) {
		return uuid.Nil, ErrReserveExists
	}
	return id, eris.Wrap(err, "create loan reserve")
}
