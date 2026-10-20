package networth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

var (
	ErrAccountNotFound   = eris.New("networth: account not found")
	ErrNotValuedByHand   = eris.New("networth: the account's value comes from its transactions or prices")
	ErrValuationNotFound = eris.New("networth: valuation not found")
	ErrNoP2PSnapshot     = eris.New("networth: the account has no imported P2P loans")
)

type Valuation struct {
	ID         uuid.UUID
	AccountID  uuid.UUID
	ValuedOn   time.Time
	ValueMinor int64
	Note       string
}

// datedValue is an account's worth from a day on: a valuation or an imported P2P snapshot.
type datedValue struct {
	on       time.Time
	value    int64
	imported bool
}

type Repo struct {
	pool *db.Pool
}

func NewRepo(pool *db.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) accounts(ctx context.Context) ([]account.Account, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name, institution, kind, currency, opening_balance_minor, color_key
		FROM account WHERE NOT archived ORDER BY name`)
	if err != nil {
		return nil, eris.Wrap(err, "list accounts")
	}
	accounts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (account.Account, error) {
		var a account.Account
		err := row.Scan(&a.ID, &a.Name, &a.Institution, &a.Kind, &a.Currency, &a.OpeningBalanceMinor, &a.ColorKey)
		return a, err
	})
	return accounts, eris.Wrap(err, "read accounts")
}

func (r *Repo) account(ctx context.Context, q db.Querier, id uuid.UUID) (*account.Account, error) {
	a := &account.Account{ID: id}
	err := q.QueryRow(ctx, `SELECT name, kind, currency FROM account WHERE id = $1`, id).Scan(&a.Name, &a.Kind, &a.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	return a, eris.Wrap(err, "get account")
}

// ledgerBalances is each account's opening balance plus its transactions to the end of each day,
// summed between consecutive days and accumulated, so each transaction is read once.
func (r *Repo) ledgerBalances(ctx context.Context, days []time.Time) (map[uuid.UUID]map[time.Time]int64, error) {
	rows, err := r.pool.Query(ctx, `
		WITH d AS (
			SELECT day, coalesce(lag(day) OVER (ORDER BY day), '-infinity') AS after FROM unnest($1::date[]) AS u (day)
		)
		SELECT a.id, d.day,
			(a.opening_balance_minor + sum(coalesce(m.amount, 0)) OVER (PARTITION BY a.id ORDER BY d.day))::bigint
		FROM account a CROSS JOIN d
		LEFT JOIN LATERAL (
			SELECT sum(t.amount_minor) AS amount FROM transaction t
			WHERE t.account_id = a.id AND t.booked_on > d.after AND t.booked_on <= d.day
		) m ON true
		WHERE NOT a.archived`, days)
	if err != nil {
		return nil, eris.Wrap(err, "sum balances on days")
	}
	defer rows.Close()
	out := map[uuid.UUID]map[time.Time]int64{}
	for rows.Next() {
		var id uuid.UUID
		var day time.Time
		var balance int64
		if err := rows.Scan(&id, &day, &balance); err != nil {
			return nil, eris.Wrap(err, "read balance on day")
		}
		if out[id] == nil {
			out[id] = map[time.Time]int64{}
		}
		out[id][day] = balance
	}
	return out, eris.Wrap(rows.Err(), "read balances on days")
}

// datedValues are every valuation and P2P snapshot total, oldest first per account.
func (r *Repo) datedValues(ctx context.Context) (map[uuid.UUID][]datedValue, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT account_id, valued_on, value_minor, false FROM account_valuation
		UNION ALL
		SELECT account_id, snapshot_on, sum(outstanding_minor)::bigint, true FROM p2p_position GROUP BY account_id, snapshot_on
		ORDER BY 1, 2, 4 DESC`)
	if err != nil {
		return nil, eris.Wrap(err, "list valuations")
	}
	defer rows.Close()
	out := map[uuid.UUID][]datedValue{}
	for rows.Next() {
		var id uuid.UUID
		var v datedValue
		if err := rows.Scan(&id, &v.on, &v.value, &v.imported); err != nil {
			return nil, eris.Wrap(err, "read valuation")
		}
		out[id] = append(out[id], v)
	}
	return out, eris.Wrap(rows.Err(), "read valuations")
}

func (r *Repo) valuations(ctx context.Context, accountID uuid.UUID) ([]Valuation, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, account_id, valued_on, value_minor, note FROM account_valuation
		WHERE account_id = $1 ORDER BY valued_on DESC`, accountID)
	if err != nil {
		return nil, eris.Wrap(err, "list account valuations")
	}
	valuations, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Valuation, error) {
		var v Valuation
		err := row.Scan(&v.ID, &v.AccountID, &v.ValuedOn, &v.ValueMinor, &v.Note)
		return v, err
	})
	return valuations, eris.Wrap(err, "read account valuations")
}

func (r *Repo) saveValuation(ctx context.Context, q db.Querier, v *Valuation) error {
	err := q.QueryRow(ctx, `
		INSERT INTO account_valuation (account_id, valued_on, value_minor, note) VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id, valued_on) DO UPDATE SET value_minor = EXCLUDED.value_minor, note = EXCLUDED.note
		RETURNING id`, v.AccountID, v.ValuedOn, v.ValueMinor, v.Note).Scan(&v.ID)
	return eris.Wrap(err, "save account valuation")
}

func (r *Repo) deleteValuation(ctx context.Context, q db.Querier, accountID, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM account_valuation WHERE id = $1 AND account_id = $2`, id, accountID)
	if err != nil {
		return eris.Wrap(err, "delete account valuation")
	}
	if tag.RowsAffected() == 0 {
		return ErrValuationNotFound
	}
	return nil
}

// Flow is money that moved into an account (positive) or out of it, seen from the other own
// account's statement: the account itself has no statement of its own.
type Flow struct {
	On          time.Time
	AmountMinor int64
}

func (r *Repo) flows(ctx context.Context, accountID uuid.UUID, currency money.Currency) ([]Flow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.booked_on, -t.amount_minor FROM transfer_leg tl JOIN transaction t ON t.id = tl.transaction_id
		WHERE tl.counterpart_account_id = $1 AND t.account_id <> $1 AND t.currency = $2
		ORDER BY 1`, accountID, string(currency))
	if err != nil {
		return nil, eris.Wrap(err, "list account flows")
	}
	flows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Flow, error) {
		var f Flow
		err := row.Scan(&f.On, &f.AmountMinor)
		return f, err
	})
	return flows, eris.Wrap(err, "read account flows")
}
