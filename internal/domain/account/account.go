package account

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/bank"
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
	Cash    Kind = "cash"
	Card    Kind = "card"
)

var (
	ErrNotFound         = eris.New("account: not found")
	ErrCurrencyMismatch = eris.New("account: accounts hold different currencies")
	ErrMergeIntoItself  = eris.New("account: an account cannot be merged into itself")
	ErrAliasNotFound    = eris.New("account: alias not found")
)

// manualRefPrefix marks accounts created by hand or for a file that names no account.
const manualRefPrefix = "manual:"

func ManualRef() string { return manualRefPrefix + uuid.NewString() }

// IsManual reports an account no bank statement identifies, so nothing imports into it by identity.
func (a *Account) IsManual() bool {
	return a.AccountNumber == nil && a.ExternalRef != nil && strings.HasPrefix(*a.ExternalRef, manualRefPrefix)
}

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
	// BankID points into the bank register; nil when the bank is not known.
	BankID    *string
	Archived  bool
	CreatedAt time.Time
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
	pool *db.Pool
}

func NewRepo(pool *db.Pool) *Repo { return &Repo{pool: pool} }

const columns = `a.id, a.name, a.institution, a.kind, a.currency, a.account_number, a.external_ref, a.parent_id,
	a.opening_balance_minor, a.color_key, a.bank_id, a.archived, a.created_at`

func scan(row pgx.Row, extra ...any) (*Account, error) {
	a := &Account{}
	dest := append([]any{&a.ID, &a.Name, &a.Institution, &a.Kind, &a.Currency, &a.AccountNumber, &a.ExternalRef,
		&a.ParentID, &a.OpeningBalanceMinor, &a.ColorKey, &a.BankID, &a.Archived, &a.CreatedAt}, extra...)
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
	return r.GetIn(ctx, r.pool, id)
}

func (r *Repo) GetIn(ctx context.Context, q db.Querier, id uuid.UUID) (*Account, error) {
	a, err := scan(q.QueryRow(ctx, `SELECT `+columns+` FROM account a WHERE a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, eris.Wrap(err, "get account")
}

// FindByIdentity looks an account up by bank number or external reference.
// A nil result with a nil error means no such account exists yet.
func (r *Repo) FindByIdentity(ctx context.Context, q db.Querier, number, externalRef *string) (*Account, error) {
	var canonical *string
	if number != nil {
		c := bank.CanonicalNumber(*number)
		canonical = &c
	}
	a, err := scan(q.QueryRow(ctx, `SELECT `+columns+` FROM account a
		WHERE ($1::text IS NOT NULL AND a.account_number = ANY(ARRAY[$1, $3])) OR ($2::text IS NOT NULL AND a.external_ref = $2)
		LIMIT 1`, number, externalRef, canonical))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, eris.Wrap(err, "find account")
}

func (r *Repo) Create(ctx context.Context, q db.Querier, a *Account) error {
	err := q.QueryRow(ctx, `
		INSERT INTO account (name, institution, kind, currency, account_number, external_ref, parent_id, opening_balance_minor, bank_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id, created_at`,
		a.Name, a.Institution, a.Kind, a.Currency, a.AccountNumber, a.ExternalRef, a.ParentID, a.OpeningBalanceMinor, a.BankID).
		Scan(&a.ID, &a.CreatedAt)
	return eris.Wrap(err, "create account")
}

type Update struct {
	Name                *string
	Kind                *Kind
	OpeningBalanceMinor *int64
	ColorKey            *string
	Archived            *bool
	// BankID: nil keeps the bank, a pointer to "" clears it.
	BankID *string
}

func (r *Repo) Update(ctx context.Context, id uuid.UUID, u Update) (*Account, error) {
	a, err := scan(r.pool.QueryRow(ctx, `
		UPDATE account a SET
			name = coalesce($2, name),
			kind = coalesce($3, kind),
			opening_balance_minor = coalesce($4, opening_balance_minor),
			color_key = coalesce($5, color_key),
			archived = coalesce($6, archived),
			bank_id = CASE WHEN $7::text IS NULL THEN bank_id ELSE nullif($7, '') END
		WHERE a.id = $1 RETURNING `+columns, id, u.Name, u.Kind, u.OpeningBalanceMinor, u.ColorKey, u.Archived, u.BankID))
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

type Alias struct {
	ID          uuid.UUID
	Institution Institution
	Identity    string
	AccountID   uuid.UUID
	CreatedAt   time.Time
}

// FindAlias returns the account a bank account was mapped to by hand; nil when none.
func (r *Repo) FindAlias(ctx context.Context, q db.Querier, institution, identity string) (*Account, error) {
	a, err := scan(q.QueryRow(ctx, `SELECT `+columns+` FROM account a
		JOIN account_alias al ON al.account_id = a.id WHERE al.institution = $1 AND al.identity = $2`, institution, identity))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, eris.Wrap(err, "find account alias")
}

func (r *Repo) UpsertAlias(ctx context.Context, q db.Querier, institution, identity string, accountID uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO account_alias (institution, identity, account_id) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, institution, identity) DO UPDATE SET account_id = EXCLUDED.account_id`, institution, identity, accountID)
	return eris.Wrap(err, "upsert account alias")
}

func (r *Repo) Aliases(ctx context.Context) ([]Alias, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, institution, identity, account_id, created_at FROM account_alias ORDER BY created_at DESC`)
	if err != nil {
		return nil, eris.Wrap(err, "list account aliases")
	}
	aliases, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Alias, error) {
		var a Alias
		err := row.Scan(&a.ID, &a.Institution, &a.Identity, &a.AccountID, &a.CreatedAt)
		return a, err
	})
	return aliases, eris.Wrap(err, "read account aliases")
}

func (r *Repo) DeleteAlias(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM account_alias WHERE id = $1`, id)
	if err != nil {
		return eris.Wrap(err, "delete account alias")
	}
	if tag.RowsAffected() == 0 {
		return ErrAliasNotFound
	}
	return nil
}

// Merge moves everything from source into target and deletes source. Future imports
// of the source bank account land in target through an alias.
func (r *Repo) Merge(ctx context.Context, sourceID, targetID uuid.UUID) (int, error) {
	if sourceID == targetID {
		return 0, ErrMergeIntoItself
	}
	var moved int64
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		source, err := r.GetIn(ctx, tx, sourceID)
		if err != nil {
			return err
		}
		target, err := r.GetIn(ctx, tx, targetID)
		if err != nil {
			return err
		}
		if source.Currency != target.Currency {
			return ErrCurrencyMismatch
		}
		tag, err := tx.Exec(ctx, `UPDATE transaction SET account_id = $2 WHERE account_id = $1`, sourceID, targetID)
		if err != nil {
			return eris.Wrap(err, "move transactions")
		}
		moved = tag.RowsAffected()
		// A transfer between the two accounts is now a move within one account: no longer a transfer.
		if _, err := tx.Exec(ctx, `DELETE FROM transfer_pair tp USING transaction o, transaction i
			WHERE tp.out_tx_id = o.id AND tp.in_tx_id = i.id AND o.account_id = i.account_id`); err != nil {
			return eris.Wrap(err, "drop internal transfers")
		}
		if _, err := tx.Exec(ctx, `UPDATE account_alias SET account_id = $2 WHERE account_id = $1`, sourceID, targetID); err != nil {
			return eris.Wrap(err, "move aliases")
		}
		if err := retargetOwnTransfers(ctx, tx, sourceID, targetID); err != nil {
			return err
		}
		if !source.IsManual() {
			if err := r.UpsertAlias(ctx, tx, string(source.Institution), source.Identity(), targetID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE account SET parent_id = NULL WHERE parent_id = $1`, sourceID); err != nil {
			return eris.Wrap(err, "detach pockets")
		}
		if _, err := tx.Exec(ctx, `UPDATE account SET opening_balance_minor = opening_balance_minor + $2 WHERE id = $1`,
			targetID, source.OpeningBalanceMinor); err != nil {
			return eris.Wrap(err, "combine opening balances")
		}
		_, err = tx.Exec(ctx, `DELETE FROM account WHERE id = $1`, sourceID)
		return eris.Wrap(err, "delete merged account")
	})
	return int(moved), err
}

// retargetOwnTransfers points one-leg transfers at the merged account; those that now
// start and end in the same account stop being transfers.
func retargetOwnTransfers(ctx context.Context, tx pgx.Tx, sourceID, targetID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE own_transfer SET account_id = $2 WHERE account_id = $1`, sourceID, targetID); err != nil {
		return eris.Wrap(err, "move own transfers")
	}
	if _, err := tx.Exec(ctx, `UPDATE own_transfer_rule SET account_id = $2 WHERE account_id = $1`, sourceID, targetID); err != nil {
		return eris.Wrap(err, "move own transfer rules")
	}
	_, err := tx.Exec(ctx, `DELETE FROM own_transfer ot USING transaction t
		WHERE t.id = ot.transaction_id AND t.account_id = ot.account_id`)
	return eris.Wrap(err, "drop internal own transfers")
}
