// Package spending aggregates transactions by main category for a period, the
// shared base of budgets and reports. Transfers are excluded (F-PRE-3).
package spending

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/categorization"
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
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Totals sums signed amounts per month and category over [from, to). An
// uncategorised transaction counts as income or expense by its sign.
func (r *Repo) Totals(ctx context.Context, from, to time.Time, currency money.Currency) ([]Total, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT date_trunc('month', t.booked_on)::date, c.id, m.id,
			coalesce(m.kind, CASE WHEN t.amount_minor > 0 THEN 'income' ELSE 'expense' END),
			sum(coalesce(ca.amount_minor, t.amount_minor))::bigint
		FROM transaction t
		LEFT JOIN category_assignment ca ON ca.transaction_id = t.id
		LEFT JOIN category c ON c.id = ca.category_id
		LEFT JOIN category m ON m.id = coalesce(c.parent_id, c.id)
		WHERE t.booked_on >= $1 AND t.booked_on < $2 AND t.currency = $3
		  AND NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.out_tx_id = t.id OR tp.in_tx_id = t.id)
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

func MonthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func DaysIn(month time.Time) int {
	return MonthStart(month).AddDate(0, 1, -1).Day()
}
