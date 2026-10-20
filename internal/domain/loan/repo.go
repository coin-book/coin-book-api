package loan

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

var (
	ErrNotALoan       = eris.New("loan: the account is not a mortgage or a loan")
	ErrTermsNotFound  = eris.New("loan: no terms from that day")
	ErrNotAPayment    = eris.New("loan: the transaction cannot pay this loan")
	ErrPaymentMissing = eris.New("loan: the transaction is not a loan payment")
	ErrSplitMismatch  = eris.New("loan: interest and principal do not add up to the payment")
	ErrExtraTooLarge  = eris.New("loan: an extra payment of the whole balance repays the loan")
)

// interestCategory is where a payment's interest is spent; principal goes to repayments.
var interestCategory = map[account.Kind]string{account.Mortgage: "housing.mortgage_interest", account.Loan: "services.bank_fees"}

const principalCategory = "financial.loan_repayment"

type Loan struct {
	AccountID uuid.UUID
	Name      string
	Kind      account.Kind
	Currency  money.Currency
}

type PaymentRecord struct {
	Payment
	Description string
	Split       Split
}

type Repo struct {
	pool *db.Pool
}

func NewRepo(pool *db.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) loan(ctx context.Context, q db.Querier, accountID uuid.UUID) (*Loan, error) {
	l := &Loan{AccountID: accountID}
	err := q.QueryRow(ctx, `SELECT name, kind, currency FROM account WHERE id = $1`, accountID).Scan(&l.Name, &l.Kind, &l.Currency)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !l.Kind.IsLiability()) {
		return nil, ErrNotALoan
	}
	return l, eris.Wrap(err, "get loan account")
}

func (r *Repo) loans(ctx context.Context, q db.Querier) ([]Loan, error) {
	rows, err := q.Query(ctx, `SELECT id, name, kind, currency FROM account WHERE kind IN ('mortgage', 'loan') ORDER BY name`)
	if err != nil {
		return nil, eris.Wrap(err, "list loans")
	}
	loans, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Loan, error) {
		var l Loan
		err := row.Scan(&l.AccountID, &l.Name, &l.Kind, &l.Currency)
		return l, err
	})
	return loans, eris.Wrap(err, "read loans")
}

func (r *Repo) terms(ctx context.Context, q db.Querier, accountID uuid.UUID) ([]Terms, error) {
	rows, err := q.Query(ctx, `SELECT valid_from, balance_minor, annual_rate::text, payment_minor, payment_day, maturity_on, fixed_until
		FROM loan_terms WHERE account_id = $1 ORDER BY valid_from`, accountID)
	if err != nil {
		return nil, eris.Wrap(err, "list loan terms")
	}
	terms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Terms, error) {
		var t Terms
		var rate string
		if err := row.Scan(&t.ValidFrom, &t.BalanceMinor, &rate, &t.PaymentMinor, &t.PaymentDay, &t.MaturityOn, &t.FixedUntil); err != nil {
			return t, err
		}
		t.AnnualRate, err = decimal.NewFromString(rate)
		return t, err
	})
	return terms, eris.Wrap(err, "read loan terms")
}

func (r *Repo) saveTerms(ctx context.Context, q db.Querier, accountID uuid.UUID, t Terms) error {
	_, err := q.Exec(ctx, `
		INSERT INTO loan_terms (account_id, valid_from, balance_minor, annual_rate, payment_minor, payment_day, maturity_on, fixed_until)
		VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8)
		ON CONFLICT (account_id, valid_from) DO UPDATE SET balance_minor = EXCLUDED.balance_minor,
			annual_rate = EXCLUDED.annual_rate, payment_minor = EXCLUDED.payment_minor, payment_day = EXCLUDED.payment_day,
			maturity_on = EXCLUDED.maturity_on, fixed_until = EXCLUDED.fixed_until`,
		accountID, t.ValidFrom, t.BalanceMinor, t.AnnualRate.String(), t.PaymentMinor, t.PaymentDay, t.MaturityOn, t.FixedUntil)
	return eris.Wrap(err, "save loan terms")
}

func (r *Repo) deleteTerms(ctx context.Context, q db.Querier, accountID uuid.UUID, validFrom time.Time) error {
	tag, err := q.Exec(ctx, `DELETE FROM loan_terms WHERE account_id = $1 AND valid_from = $2`, accountID, validFrom)
	if err != nil {
		return eris.Wrap(err, "delete loan terms")
	}
	if tag.RowsAffected() == 0 {
		return ErrTermsNotFound
	}
	return nil
}

// anchors are balances the user entered on the loan account.
func (r *Repo) anchors(ctx context.Context, q db.Querier, accountID uuid.UUID) ([]Anchor, error) {
	rows, err := q.Query(ctx, `SELECT valued_on, value_minor FROM account_valuation WHERE account_id = $1 ORDER BY valued_on`, accountID)
	if err != nil {
		return nil, eris.Wrap(err, "list loan balances")
	}
	anchors, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Anchor, error) {
		var a Anchor
		err := row.Scan(&a.On, &a.BalanceMinor)
		return a, err
	})
	return anchors, eris.Wrap(err, "read loan balances")
}

func (r *Repo) payments(ctx context.Context, q db.Querier, accountID uuid.UUID) ([]PaymentRecord, error) {
	rows, err := q.Query(ctx, `
		SELECT t.id, t.booked_on, -t.amount_minor, t.description, lp.interest_minor, lp.principal_minor, lp.by_schedule
		FROM loan_payment lp JOIN transaction t ON t.id = lp.transaction_id
		WHERE lp.account_id = $1 ORDER BY t.booked_on, t.id`, accountID)
	if err != nil {
		return nil, eris.Wrap(err, "list loan payments")
	}
	payments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PaymentRecord, error) {
		var p PaymentRecord
		var bySchedule bool
		if err := row.Scan(&p.TransactionID, &p.BookedOn, &p.AmountMinor, &p.Description, &p.Split.InterestMinor,
			&p.Split.PrincipalMinor, &bySchedule); err != nil {
			return p, err
		}
		if !bySchedule {
			split := p.Split
			p.Fixed = &split
		}
		return p, nil
	})
	return payments, eris.Wrap(err, "read loan payments")
}

// adoptOwnTransfers turns outgoing one-leg transfers to a loan, paid since its first terms and
// in its currency, into payments split by the schedule. Categories the payments carried go.
func (r *Repo) adoptOwnTransfers(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `
		WITH adopted AS (
			DELETE FROM own_transfer ot USING transaction t, account a
			WHERE t.id = ot.transaction_id AND a.id = ot.account_id AND a.kind IN ('mortgage', 'loan')
			  AND t.amount_minor < 0 AND t.currency = a.currency
			  AND t.booked_on >= (SELECT min(lt.valid_from) FROM loan_terms lt WHERE lt.account_id = a.id)
			RETURNING ot.transaction_id, ot.account_id
		)
		, inserted AS (
			INSERT INTO loan_payment (transaction_id, account_id, interest_minor, principal_minor, by_schedule)
			SELECT transaction_id, account_id, 0, 0, true FROM adopted
			ON CONFLICT (transaction_id) DO NOTHING
			RETURNING transaction_id
		)
		-- A category given before the payment was known to pay the loan gives way to its split.
		DELETE FROM category_assignment WHERE transaction_id IN (SELECT transaction_id FROM inserted)`)
	return eris.Wrap(err, "adopt loan payments")
}

// writeSplits stores the payments' splits and gives each its interest and principal category lines.
func (r *Repo) writeSplits(ctx context.Context, q db.Querier, l Loan, splits map[string]Split) error {
	if len(splits) == 0 {
		return nil
	}
	ids := make([]string, 0, len(splits))
	interest := make([]int64, 0, len(splits))
	principal := make([]int64, 0, len(splits))
	for id, s := range splits {
		ids, interest, principal = append(ids, id), append(interest, s.InterestMinor), append(principal, s.PrincipalMinor)
	}
	if _, err := q.Exec(ctx, `
		UPDATE loan_payment lp SET interest_minor = s.interest, principal_minor = s.principal
		FROM unnest($1::text[], $2::bigint[], $3::bigint[]) AS s (id, interest, principal)
		WHERE lp.transaction_id = s.id AND lp.by_schedule`, ids, interest, principal); err != nil {
		return eris.Wrap(err, "update loan payment splits")
	}
	if _, err := q.Exec(ctx, `DELETE FROM category_assignment WHERE transaction_id = ANY($1) AND source = 'loan'`, ids); err != nil {
		return eris.Wrap(err, "clear loan payment categories")
	}
	// A payment the user categorised by hand keeps their lines; the split still values the loan.
	_, err := q.Exec(ctx, `
		INSERT INTO category_assignment (transaction_id, system_category_id, amount_minor, source)
		SELECT lp.transaction_id, c.id, -line.amount, 'loan'
		FROM loan_payment lp
		CROSS JOIN LATERAL (VALUES ($2::text, lp.interest_minor), ($3::text, lp.principal_minor)) AS line (code, amount)
		JOIN category c ON c.code = line.code
		WHERE lp.transaction_id = ANY($1) AND line.amount > 0
		  AND NOT EXISTS (SELECT 1 FROM category_assignment ca WHERE ca.transaction_id = lp.transaction_id)`,
		ids, interestCategory[l.Kind], principalCategory)
	return eris.Wrap(err, "categorize loan payments")
}

type transactionFacts struct {
	accountID uuid.UUID
	amount    int64
	currency  money.Currency
	paired    bool
}

func (r *Repo) transaction(ctx context.Context, q db.Querier, id string) (*transactionFacts, error) {
	f := &transactionFacts{}
	err := q.QueryRow(ctx, `SELECT t.account_id, t.amount_minor, t.currency,
			EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.out_tx_id = t.id OR tp.in_tx_id = t.id)
		FROM transaction t WHERE t.id = $1`, id).Scan(&f.accountID, &f.amount, &f.currency, &f.paired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotAPayment
	}
	return f, eris.Wrap(err, "get transaction")
}

func (r *Repo) markPayment(ctx context.Context, q db.Querier, transactionID string, accountID uuid.UUID, fixed *Split) (*uuid.UUID, error) {
	var previous *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT account_id FROM loan_payment WHERE transaction_id = $1`, transactionID).Scan(&previous); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return nil, eris.Wrap(err, "find loan payment")
	}
	for _, stmt := range []string{
		`DELETE FROM own_transfer WHERE transaction_id = $1`,
		`DELETE FROM not_transfer WHERE transaction_id = $1`,
		`DELETE FROM category_assignment WHERE transaction_id = $1`,
	} {
		if _, err := q.Exec(ctx, stmt, transactionID); err != nil {
			return nil, eris.Wrap(err, "clear transfer marks")
		}
	}
	var interest, principal int64
	if fixed != nil {
		interest, principal = fixed.InterestMinor, fixed.PrincipalMinor
	}
	_, err := q.Exec(ctx, `
		INSERT INTO loan_payment (transaction_id, account_id, interest_minor, principal_minor, by_schedule)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (transaction_id) DO UPDATE SET account_id = EXCLUDED.account_id, interest_minor = EXCLUDED.interest_minor,
			principal_minor = EXCLUDED.principal_minor, by_schedule = EXCLUDED.by_schedule`,
		transactionID, accountID, interest, principal, fixed == nil)
	return previous, eris.Wrap(err, "save loan payment")
}

// unmarkPayment also records the transaction as not a transfer, so detection by the loan's
// account number does not bring it back.
func (r *Repo) unmarkPayment(ctx context.Context, q db.Querier, transactionID string) (uuid.UUID, error) {
	var accountID uuid.UUID
	err := q.QueryRow(ctx, `DELETE FROM loan_payment WHERE transaction_id = $1 RETURNING account_id`, transactionID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return accountID, ErrPaymentMissing
	}
	if err != nil {
		return accountID, eris.Wrap(err, "delete loan payment")
	}
	if _, err := q.Exec(ctx, `DELETE FROM category_assignment WHERE transaction_id = $1`, transactionID); err != nil {
		return accountID, eris.Wrap(err, "clear loan payment categories")
	}
	_, err = q.Exec(ctx, `INSERT INTO not_transfer (transaction_id) VALUES ($1) ON CONFLICT DO NOTHING`, transactionID)
	return accountID, eris.Wrap(err, "remember not a transfer")
}
