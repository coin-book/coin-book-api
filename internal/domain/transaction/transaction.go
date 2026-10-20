package transaction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

var (
	ErrNotFound      = eris.New("transaction: not found")
	ErrInvalidCursor = eris.New("transaction: invalid cursor")
)

type Transaction struct {
	ID                  string
	AccountID           uuid.UUID
	BookedOn            time.Time
	Amount              money.Amount
	FeeMinor            int64
	Description         string
	Counterparty        *string
	CounterpartyAccount *string
	OperationType       *string
	Message             *string
	VariableSymbol      *string
	ConstantSymbol      *string
	SpecificSymbol      *string
	BankCategory        *string
	BalanceAfterMinor   *int64
	ImportBatchID       uuid.UUID
	SourceLine          int
}

type Assignment struct {
	CategoryID  uuid.UUID `json:"category_id"`
	AmountMinor int64     `json:"amount_minor"`
	Source      string    `json:"source"`
}

type Transfer struct {
	// CounterpartTxID is nil while the other account's statement is not imported.
	CounterpartTxID      *string   `json:"counterpart_tx_id"`
	CounterpartAccountID uuid.UUID `json:"counterpart_account_id"`
	MatchedBy            string    `json:"matched_by"`
}

// LoanPayment is the transaction paying an instalment of an own mortgage or loan (ADR 211).
type LoanPayment struct {
	AccountID      uuid.UUID `json:"account_id"`
	InterestMinor  int64     `json:"interest_minor"`
	PrincipalMinor int64     `json:"principal_minor"`
	BySchedule     bool      `json:"by_schedule"`
}

type Listed struct {
	Transaction
	Assignments []Assignment
	Transfer    *Transfer
	LoanPayment *LoanPayment
}

type Filter struct {
	From          *time.Time
	To            *time.Time
	AccountID     *uuid.UUID
	CategoryID    *uuid.UUID
	Query         string
	Uncategorized bool
	// AutoAssigned keeps only transactions the classifier categorised, awaiting confirmation.
	AutoAssigned bool
	Cursor       string
	Limit        int
}

type Page struct {
	Items      []Listed
	NextCursor string
}

type Repo struct {
	pool *db.Pool
}

func NewRepo(pool *db.Pool) *Repo { return &Repo{pool: pool} }

const listQuery = `
	SELECT t.id, t.account_id, t.booked_on, t.amount_minor, t.currency, t.fee_minor, t.description, t.counterparty,
		t.counterparty_account, t.operation_type, t.message, t.variable_symbol, t.constant_symbol, t.specific_symbol,
		t.bank_category, t.balance_after_minor, t.import_batch_id, t.source_line,
		coalesce((SELECT json_agg(json_build_object('category_id', ca.category_id, 'amount_minor', ca.amount_minor, 'source', ca.source))
			FROM category_assignment ca WHERE ca.transaction_id = t.id), '[]'::json),
		(SELECT json_build_object('counterpart_tx_id', tl.counterpart_tx_id, 'counterpart_account_id', tl.counterpart_account_id,
			'matched_by', tl.matched_by)
			FROM transfer_leg tl WHERE tl.transaction_id = t.id),
		(SELECT json_build_object('account_id', lp.account_id, 'interest_minor', lp.interest_minor,
			'principal_minor', lp.principal_minor, 'by_schedule', lp.by_schedule)
			FROM loan_payment lp WHERE lp.transaction_id = t.id)
	FROM transaction t
	WHERE ($1::date IS NULL OR t.booked_on >= $1)
	  AND ($2::date IS NULL OR t.booked_on <= $2)
	  AND ($3::uuid IS NULL OR t.account_id = $3)
	  AND ($4::uuid IS NULL OR EXISTS (
		SELECT 1 FROM category_assignment ca JOIN any_category c ON c.id = ca.category_id
		WHERE ca.transaction_id = t.id AND (c.id = $4 OR c.parent_id = $4)))
	  AND ($5 = '' OR unaccent(t.description) ILIKE '%' || unaccent($5) || '%'
		OR unaccent(t.counterparty) ILIKE '%' || unaccent($5) || '%'
		OR ($12::bigint IS NOT NULL AND CASE WHEN $13 THEN t.amount_minor = $12 ELSE abs(t.amount_minor) = $12 END))
	  AND (NOT $6 OR (NOT EXISTS (SELECT 1 FROM category_assignment ca WHERE ca.transaction_id = t.id)
		AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)))
	  AND ($7::date IS NULL OR (t.booked_on, t.id) < ($7, $8))
	  AND ($10::text IS NULL OR t.id = $10)
	  AND (NOT $11 OR EXISTS (SELECT 1 FROM category_assignment ca WHERE ca.transaction_id = t.id AND ca.source = 'model'))
	ORDER BY t.booked_on DESC, t.id DESC
	LIMIT $9`

func (r *Repo) List(ctx context.Context, f Filter) (*Page, error) {
	cursorDate, cursorID, err := decodeCursor(f.Cursor)
	if err != nil {
		return nil, err
	}
	var amount *int64
	minor, signed, isAmount := searchAmount(f.Query)
	if isAmount {
		amount = &minor
	}
	rows, err := r.pool.Query(ctx, listQuery, f.From, f.To, f.AccountID, f.CategoryID, escapeLike(f.Query),
		f.Uncategorized, cursorDate, cursorID, f.Limit+1, nil, f.AutoAssigned, amount, signed)
	if err != nil {
		return nil, eris.Wrap(err, "list transactions")
	}
	items, err := pgx.CollectRows(rows, scanListed)
	if err != nil {
		return nil, eris.Wrap(err, "read transactions")
	}
	page := &Page{Items: items}
	if len(items) > f.Limit {
		page.Items = items[:f.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(last.BookedOn, last.ID)
	}
	return page, nil
}

func (r *Repo) Get(ctx context.Context, id string) (*Listed, error) {
	rows, err := r.pool.Query(ctx, listQuery, nil, nil, nil, nil, "", false, nil, "", 1, id, false, nil, false)
	if err != nil {
		return nil, eris.Wrap(err, "get transaction")
	}
	items, err := pgx.CollectRows(rows, scanListed)
	if err != nil {
		return nil, eris.Wrap(err, "read transaction")
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return &items[0], nil
}

func (r *Repo) CountUncategorized(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM transaction t
		WHERE NOT EXISTS (SELECT 1 FROM category_assignment ca WHERE ca.transaction_id = t.id)
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)`).Scan(&n)
	return n, eris.Wrap(err, "count uncategorized")
}

func scanListed(row pgx.CollectableRow) (Listed, error) {
	var l Listed
	var assignments []byte
	var transfer, loanPayment []byte
	err := row.Scan(&l.ID, &l.AccountID, &l.BookedOn, &l.Amount.Minor, &l.Amount.Currency, &l.FeeMinor, &l.Description,
		&l.Counterparty, &l.CounterpartyAccount, &l.OperationType, &l.Message, &l.VariableSymbol, &l.ConstantSymbol,
		&l.SpecificSymbol, &l.BankCategory, &l.BalanceAfterMinor, &l.ImportBatchID, &l.SourceLine, &assignments, &transfer,
		&loanPayment)
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(assignments, &l.Assignments); err != nil {
		return l, eris.Wrap(err, "decode assignments")
	}
	if len(transfer) > 0 {
		l.Transfer = &Transfer{}
		if err := json.Unmarshal(transfer, l.Transfer); err != nil {
			return l, eris.Wrap(err, "decode transfer")
		}
	}
	if len(loanPayment) > 0 {
		l.LoanPayment = &LoanPayment{}
		if err := json.Unmarshal(loanPayment, l.LoanPayment); err != nil {
			return l, eris.Wrap(err, "decode loan payment")
		}
	}
	return l, nil
}

func escapeLike(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(q))
}

func encodeCursor(date time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(date.Format(time.DateOnly) + "|" + id))
}

func decodeCursor(cursor string) (*time.Time, string, error) {
	if cursor == "" {
		return nil, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, "", ErrInvalidCursor
	}
	datePart, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, "", ErrInvalidCursor
	}
	date, err := time.Parse(time.DateOnly, datePart)
	if err != nil {
		return nil, "", ErrInvalidCursor
	}
	return &date, id, nil
}
