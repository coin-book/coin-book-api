package account

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

type Institution string

const (
	MBankCZ Institution = "mbank_cz"
	MBankSK Institution = "mbank_sk"
	Revolut Institution = "revolut"
	IBKR    Institution = "ibkr"
	Other   Institution = "other"
)

type Kind string

const (
	Current Kind = "current"
	Savings Kind = "savings"
	Pocket  Kind = "pocket"
	Fund    Kind = "fund"
	Broker  Kind = "broker"
)

var ErrNotFound = eris.New("account: not found")

type Account struct {
	ID                  uuid.UUID
	Name                string
	Institution         Institution
	Kind                Kind
	Currency            money.Currency
	AccountNumber       *string
	ExternalRef         *string
	ParentID            *uuid.UUID
	OpeningBalanceMinor int64
	ColorKey            *string
	Archived            bool
	CreatedAt           time.Time
}

// Identity is what deterministic transaction ids are derived from: the bank
// number when there is one, else the importer's external reference.
func (a *Account) Identity() string {
	if a.AccountNumber != nil {
		return *a.AccountNumber
	}
	return *a.ExternalRef
}

type WithBalance struct {
	Account
	Balance          money.Amount
	TransactionCount int
	LastBookedOn     *time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const columns = `a.id, a.name, a.institution, a.kind, a.currency, a.account_number, a.external_ref, a.parent_id,
	a.opening_balance_minor, a.color_key, a.archived, a.created_at`

func scan(row pgx.Row, extra ...any) (*Account, error) {
	a := &Account{}
	dest := append([]any{&a.ID, &a.Name, &a.Institution, &a.Kind, &a.Currency, &a.AccountNumber, &a.ExternalRef,
		&a.ParentID, &a.OpeningBalanceMinor, &a.ColorKey, &a.Archived, &a.CreatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return a, nil
}

func (r *Repo) ListWithBalances(ctx context.Context) ([]WithBalance, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+columns+`, coalesce(sum(t.amount_minor), 0), count(t.id), max(t.booked_on)
		FROM account a LEFT JOIN transaction t ON t.account_id = a.id
		GROUP BY a.id ORDER BY a.archived, a.institution, a.name`)
	if err != nil {
		return nil, eris.Wrap(err, "list accounts")
	}
	defer rows.Close()
	var out []WithBalance
	for rows.Next() {
		var sum int64
		var count int
		var last *time.Time
		a, err := scan(rows, &sum, &count, &last)
		if err != nil {
			return nil, eris.Wrap(err, "scan account")
		}
		out = append(out, WithBalance{
			Account:          *a,
			Balance:          money.New(a.OpeningBalanceMinor+sum, a.Currency),
			TransactionCount: count,
			LastBookedOn:     last,
		})
	}
	return out, eris.Wrap(rows.Err(), "read accounts")
}

func (r *Repo) Get(ctx context.Context, id uuid.UUID) (*Account, error) {
	a, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM account a WHERE a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, eris.Wrap(err, "get account")
}

// FindByIdentity looks an account up by bank number or external reference.
// A nil result with a nil error means no such account exists yet.
func (r *Repo) FindByIdentity(ctx context.Context, q db.Querier, number, externalRef *string) (*Account, error) {
	a, err := scan(q.QueryRow(ctx, `SELECT `+columns+` FROM account a
		WHERE ($1::text IS NOT NULL AND a.account_number = $1) OR ($2::text IS NOT NULL AND a.external_ref = $2)
		LIMIT 1`, number, externalRef))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, eris.Wrap(err, "find account")
}

func (r *Repo) Create(ctx context.Context, q db.Querier, a *Account) error {
	err := q.QueryRow(ctx, `
		INSERT INTO account (name, institution, kind, currency, account_number, external_ref, parent_id, opening_balance_minor)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		a.Name, a.Institution, a.Kind, a.Currency, a.AccountNumber, a.ExternalRef, a.ParentID, a.OpeningBalanceMinor).
		Scan(&a.ID, &a.CreatedAt)
	return eris.Wrap(err, "create account")
}

type Update struct {
	Name                *string
	Kind                *Kind
	OpeningBalanceMinor *int64
	ColorKey            *string
	Archived            *bool
}

func (r *Repo) Update(ctx context.Context, id uuid.UUID, u Update) (*Account, error) {
	a, err := scan(r.pool.QueryRow(ctx, `
		UPDATE account a SET
			name = coalesce($2, name),
			kind = coalesce($3, kind),
			opening_balance_minor = coalesce($4, opening_balance_minor),
			color_key = coalesce($5, color_key),
			archived = coalesce($6, archived)
		WHERE a.id = $1 RETURNING `+columns, id, u.Name, u.Kind, u.OpeningBalanceMinor, u.ColorKey, u.Archived))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, eris.Wrap(err, "update account")
}

func (r *Repo) OwnAccountNumbers(ctx context.Context, q db.Querier) (map[string]uuid.UUID, error) {
	rows, err := q.Query(ctx, `SELECT account_number, id FROM account WHERE account_number IS NOT NULL`)
	if err != nil {
		return nil, eris.Wrap(err, "list own account numbers")
	}
	defer rows.Close()
	out := map[string]uuid.UUID{}
	for rows.Next() {
		var number string
		var id uuid.UUID
		if err := rows.Scan(&number, &id); err != nil {
			return nil, eris.Wrap(err, "scan account number")
		}
		out[number] = id
	}
	return out, eris.Wrap(rows.Err(), "read account numbers")
}
