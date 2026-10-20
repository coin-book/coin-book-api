// Package spending aggregates transactions by main category for a period, the
// shared base of budgets and reports. Transfers are excluded (F-PRE-3).
package spending

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

type Total struct {
	Month time.Time
	// CategoryID is the leaf category; nil for money not categorised yet.
	CategoryID *uuid.UUID
	// MainCategoryID is the top-level category; nil for money not categorised yet.
	MainCategoryID *uuid.UUID
	Kind           categorization.Kind
	AmountMinor    int64
}

type Repo struct {
	pool *db.Pool
}

func NewRepo(pool *db.Pool) *Repo { return &Repo{pool: pool} }

// Totals sums signed amounts per month and category over [from, to). An
// uncategorised transaction counts as income or expense by its sign.
func (r *Repo) Totals(ctx context.Context, from, to time.Time, currency money.Currency) ([]Total, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT date_trunc('month', t.booked_on)::date, c.id, m.id,
			coalesce(m.kind, CASE WHEN t.amount_minor > 0 THEN 'income' ELSE 'expense' END),
			sum(coalesce(ca.amount_minor, t.amount_minor))::bigint
		FROM transaction t
		LEFT JOIN category_assignment ca ON ca.transaction_id = t.id
		LEFT JOIN any_category c ON c.id = ca.category_id
		LEFT JOIN any_category m ON m.id = coalesce(c.parent_id, c.id)
		WHERE t.booked_on >= $1 AND t.booked_on < $2 AND t.currency = $3
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)
		  AND coalesce(m.kind, '') <> 'transfer'
		GROUP BY 1, 2, 3, 4`, from, to, string(currency))
	if err != nil {
		return nil, eris.Wrap(err, "sum spending")
	}
	totals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Total, error) {
		var t Total
		err := row.Scan(&t.Month, &t.CategoryID, &t.MainCategoryID, &t.Kind, &t.AmountMinor)
		return t, err
	})
	return totals, eris.Wrap(err, "read spending")
}

type AccountFlow struct {
	AccountID   uuid.UUID
	AmountMinor int64
}

// SetAside nets, per saving account (savings, pocket, fund, broker, loan reserve), the transfers that
// reached it over [from, to): what the user put aside rather than spent.
func (r *Repo) SetAside(ctx context.Context, from, to time.Time, currency money.Currency) ([]AccountFlow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tl.counterpart_account_id, -sum(t.amount_minor)::bigint
		FROM transfer_leg tl
		JOIN transaction t ON t.id = tl.transaction_id
		JOIN account a ON a.id = tl.counterpart_account_id
		WHERE t.booked_on >= $1 AND t.booked_on < $2 AND t.currency = $3
		  AND a.kind IN ('savings', 'pocket', 'fund', 'broker', 'loan_reserve')
		GROUP BY 1 HAVING sum(t.amount_minor) <> 0
		ORDER BY 2 DESC`, from, to, string(currency))
	if err != nil {
		return nil, eris.Wrap(err, "sum set-aside money")
	}
	flows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountFlow, error) {
		var f AccountFlow
		err := row.Scan(&f.AccountID, &f.AmountMinor)
		return f, err
	})
	return flows, eris.Wrap(err, "read set-aside money")
}

func MonthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func DaysIn(month time.Time) int {
	return MonthStart(month).AddDate(0, 1, -1).Day()
}

// AccountTotal is one account's signed total per main category over a period.
type AccountTotal struct {
	AccountID      uuid.UUID
	MainCategoryID *uuid.UUID
	Kind           categorization.Kind
	AmountMinor    int64
}

// ByAccount sums signed amounts per account and main category over [from, to), the
// same way Totals does, for drawing where money came in and went out.
func (r *Repo) ByAccount(ctx context.Context, from, to time.Time, currency money.Currency) ([]AccountTotal, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.account_id, m.id,
			coalesce(m.kind, CASE WHEN t.amount_minor > 0 THEN 'income' ELSE 'expense' END),
			sum(coalesce(ca.amount_minor, t.amount_minor))::bigint
		FROM transaction t
		LEFT JOIN category_assignment ca ON ca.transaction_id = t.id
		LEFT JOIN any_category c ON c.id = ca.category_id
		LEFT JOIN any_category m ON m.id = coalesce(c.parent_id, c.id)
		WHERE t.booked_on >= $1 AND t.booked_on < $2 AND t.currency = $3
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)
		  AND coalesce(m.kind, '') <> 'transfer'
		GROUP BY 1, 2, 3`, from, to, string(currency))
	if err != nil {
		return nil, eris.Wrap(err, "sum spending by account")
	}
	totals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountTotal, error) {
		var t AccountTotal
		err := row.Scan(&t.AccountID, &t.MainCategoryID, &t.Kind, &t.AmountMinor)
		return t, err
	})
	return totals, eris.Wrap(err, "read spending by account")
}

// SavingTransfer is money moved from one own account into a saving account.
type SavingTransfer struct {
	FromAccountID uuid.UUID
	ToAccountID   uuid.UUID
	AmountMinor   int64
}

// SavingTransfers nets, per pair of accounts, what moved into saving accounts over
// [from, to): the set-aside flows, with the account they left.
func (r *Repo) SavingTransfers(ctx context.Context, from, to time.Time, currency money.Currency) ([]SavingTransfer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.account_id, tl.counterpart_account_id, -sum(t.amount_minor)::bigint
		FROM transfer_leg tl
		JOIN transaction t ON t.id = tl.transaction_id
		JOIN account a ON a.id = tl.counterpart_account_id
		WHERE t.booked_on >= $1 AND t.booked_on < $2 AND t.currency = $3
		  AND a.kind IN ('savings', 'pocket', 'fund', 'broker', 'loan_reserve')
		GROUP BY 1, 2 HAVING sum(t.amount_minor) < 0
		ORDER BY 3 DESC`, from, to, string(currency))
	if err != nil {
		return nil, eris.Wrap(err, "sum saving transfers")
	}
	flows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SavingTransfer, error) {
		var f SavingTransfer
		err := row.Scan(&f.FromAccountID, &f.ToAccountID, &f.AmountMinor)
		return f, err
	})
	return flows, eris.Wrap(err, "read saving transfers")
}
