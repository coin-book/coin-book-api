// Package importing turns a bank export into transactions: detect, parse,
// verify, deduplicate and write atomically (F-IMP).
package importing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/importing/parser/mbankcz"
	"coin-book-api/internal/domain/importing/parser/revolut"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

const detectHeadBytes = 64 * 1024

const previewRowLimit = 500

var (
	ErrUnsupportedFormat = eris.New("importing: unsupported file format")
	ErrChecksFailed      = eris.New("importing: the statement does not match its own control totals")
	ErrAlreadyImported   = eris.New("importing: this file has already been imported")
	ErrBatchNotFound     = eris.New("importing: import not found")
	ErrAlreadyRolledBack = eris.New("importing: import already rolled back")
)

// parsers is the explicit registry; adding a bank means adding it here.
var parsers = []statement.Parser{mbankcz.Parser{}, revolut.Parser{}}

type Categorizer interface {
	CategorizeNew(ctx context.Context, q db.Querier, candidates []categorization.Candidate) (categorization.AutoStats, error)
}

type TransferMatcher interface {
	AutoMatch(ctx context.Context, q db.Querier, transactionIDs []string) (int, error)
}

type Service struct {
	pool        *pgxpool.Pool
	accounts    *account.Repo
	categorizer Categorizer
	transfers   TransferMatcher
	importsDir  string
}

func NewService(pool *pgxpool.Pool, accounts *account.Repo, categorizer Categorizer, transfers TransferMatcher, importsDir string) *Service {
	return &Service{pool: pool, accounts: accounts, categorizer: categorizer, transfers: transfers, importsDir: importsDir}
}

type RowStatus string

const (
	RowNew       RowStatus = "new"
	RowDuplicate RowStatus = "duplicate"
)

type PreviewRow struct {
	AccountRef  string
	Line        int
	BookedOn    time.Time
	Amount      money.Amount
	Description string
	Status      RowStatus
}

type AccountResult struct {
	Ref        statement.AccountRef
	AccountID  *uuid.UUID
	IsNew      bool
	Rows       int
	New        int
	Duplicates int
	Checks     []Check
}

type Result struct {
	BatchID          *uuid.UUID
	FileName         string
	FileHash         string
	Format           statement.Format
	PeriodFrom       *time.Time
	PeriodTo         *time.Time
	AlreadyImported  bool
	ChecksPassed     bool
	Accounts         []AccountResult
	Rows             []PreviewRow
	RowsTotal        int
	RowsNew          int
	RowsDuplicate    int
	CategorizedRule  int
	CategorizedBank  int
	TransfersMatched int
}

type plannedRow struct {
	id      string
	account int
	row     statement.Row
}

type plan struct {
	result   *Result
	st       *statement.Statement
	accounts []*statement.Account
	existing []*account.Account
	rows     []plannedRow
	content  []byte
}

// Preview runs the whole pipeline without writing anything (F-IMP-4 dry-run).
func (s *Service) Preview(ctx context.Context, fileName string, content []byte) (*Result, error) {
	p, err := s.plan(ctx, s.pool, fileName, content)
	if err != nil {
		return nil, err
	}
	return p.result, nil
}

// Commit writes the statement in one transaction. Failing control checks stop it
// unless force is set, which is recorded on the batch.
func (s *Service) Commit(ctx context.Context, fileName string, content []byte, force bool) (*Result, error) {
	var result *Result
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('coinbook.import'))`); err != nil {
			return eris.Wrap(err, "lock imports")
		}
		p, err := s.plan(ctx, tx, fileName, content)
		if err != nil {
			return err
		}
		result = p.result
		if p.result.AlreadyImported {
			return ErrAlreadyImported
		}
		if !p.result.ChecksPassed && !force {
			return ErrChecksFailed
		}
		return s.write(ctx, tx, p, force)
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) plan(ctx context.Context, q db.Querier, fileName string, content []byte) (*plan, error) {
	parser := detect(content)
	if parser == nil {
		return nil, ErrUnsupportedFormat
	}
	st, err := parser.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, eris.Wrapf(ErrUnsupportedFormat, "%s: %v", parser.Format(), err)
	}

	sum := sha256.Sum256(content)
	p := &plan{st: st, content: content, result: &Result{
		FileName: filepath.Base(fileName), FileHash: hex.EncodeToString(sum[:]),
		Format: st.Format, PeriodFrom: st.PeriodFrom, PeriodTo: st.PeriodTo, ChecksPassed: true,
	}}
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM import_batch WHERE file_hash = $1 AND status = 'committed')`,
		p.result.FileHash).Scan(&p.result.AlreadyImported); err != nil {
		return nil, eris.Wrap(err, "check previous import")
	}

	for _, acc := range st.Accounts {
		if len(acc.Rows) == 0 {
			continue
		}
		existing, err := s.accounts.FindByIdentity(ctx, q, acc.Ref.AccountNumber, acc.Ref.ExternalRef)
		if err != nil {
			return nil, err
		}
		index := len(p.accounts)
		p.accounts = append(p.accounts, acc)
		p.existing = append(p.existing, existing)

		ar := AccountResult{Ref: acc.Ref, IsNew: existing == nil, Rows: len(acc.Rows), Checks: runChecks(acc)}
		if existing != nil {
			ar.AccountID = &existing.ID
		}
		p.result.ChecksPassed = p.result.ChecksPassed && allPassed(ar.Checks)
		p.result.Accounts = append(p.result.Accounts, ar)
		for i, id := range transactionIDs(acc) {
			p.rows = append(p.rows, plannedRow{id: id, account: index, row: acc.Rows[i]})
		}
	}

	known, err := existingIDs(ctx, q, p.rows)
	if err != nil {
		return nil, err
	}
	for _, r := range p.rows {
		status := RowNew
		if known[r.id] {
			status = RowDuplicate
			p.result.Accounts[r.account].Duplicates++
			p.result.RowsDuplicate++
		} else {
			p.result.Accounts[r.account].New++
			p.result.RowsNew++
		}
		if len(p.result.Rows) < previewRowLimit {
			acc := p.accounts[r.account]
			p.result.Rows = append(p.result.Rows, PreviewRow{
				AccountRef: acc.Ref.Identity(), Line: r.row.Line, BookedOn: r.row.BookedOn,
				Amount: money.New(r.row.AmountMinor, acc.Ref.Currency), Description: r.row.Description, Status: status,
			})
		}
	}
	p.result.RowsTotal = len(p.rows)
	return p, nil
}

func detect(content []byte) statement.Parser {
	head := content[:min(len(content), detectHeadBytes)]
	var best statement.Parser
	bestConfidence := statement.NoMatch
	for _, parser := range parsers {
		if c := parser.Detect(head); c > bestConfidence {
			best, bestConfidence = parser, c
		}
	}
	return best
}

// transactionIDs derives deterministic ids: the same row in a re-imported or
// overlapping export always hashes to the same id (F-IMP-2). Identical rows on one
// account are told apart by their order of occurrence.
func transactionIDs(acc *statement.Account) []string {
	seen := map[string]int{}
	ids := make([]string, len(acc.Rows))
	for i, r := range acc.Rows {
		key := acc.Ref.Identity() + "|" + r.IdentityKey
		occurrence := seen[key]
		seen[key]++
		sum := sha256.Sum256([]byte(key + "|" + strconv.Itoa(occurrence)))
		ids[i] = "tx_" + hex.EncodeToString(sum[:16])
	}
	return ids
}

func existingIDs(ctx context.Context, q db.Querier, rows []plannedRow) (map[string]bool, error) {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	result, err := q.Query(ctx, `SELECT id FROM transaction WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, eris.Wrap(err, "find existing transactions")
	}
	found, err := pgx.CollectRows(result, pgx.RowTo[string])
	if err != nil {
		return nil, eris.Wrap(err, "read existing transactions")
	}
	known := make(map[string]bool, len(found))
	for _, id := range found {
		known[id] = true
	}
	return known, nil
}

func (s *Service) write(ctx context.Context, tx pgx.Tx, p *plan, forced bool) error {
	if err := s.storeOriginal(p.result.FileHash, p.content); err != nil {
		return err
	}
	accountIDs, err := s.ensureAccounts(ctx, tx, p)
	if err != nil {
		return err
	}
	report, err := json.Marshal(p.result.Accounts)
	if err != nil {
		return eris.Wrap(err, "encode check report")
	}
	var batchID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO import_batch (file_name, file_hash, format, parser_version, period_from, period_to, status, forced,
			rows_total, rows_inserted, rows_duplicate, check_report)
		VALUES ($1, $2, $3, $4, $5, $6, 'committed', $7, $8, $9, $10, $11) RETURNING id`,
		p.result.FileName, p.result.FileHash, string(p.st.Format), p.st.Version, p.st.PeriodFrom, p.st.PeriodTo,
		forced && !p.result.ChecksPassed, p.result.RowsTotal, p.result.RowsNew, p.result.RowsDuplicate, report).Scan(&batchID)
	if err != nil {
		return eris.Wrap(err, "insert import batch")
	}
	p.result.BatchID = &batchID

	inserted, err := insertTransactions(ctx, tx, p, accountIDs, batchID)
	if err != nil {
		return err
	}
	stats, err := s.categorizer.CategorizeNew(ctx, tx, inserted)
	if err != nil {
		return err
	}
	p.result.CategorizedRule, p.result.CategorizedBank = stats.ByRule, stats.ByBank

	ids := make([]string, len(inserted))
	for i, c := range inserted {
		ids[i] = c.TransactionID
	}
	p.result.TransfersMatched, err = s.transfers.AutoMatch(ctx, tx, ids)
	return err
}

func (s *Service) storeOriginal(hash string, content []byte) error {
	if err := os.MkdirAll(s.importsDir, 0o700); err != nil {
		return eris.Wrap(err, "create imports directory")
	}
	path := filepath.Join(s.importsDir, hash)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return eris.Wrap(os.WriteFile(path, content, 0o600), "store original file")
}

func (s *Service) ensureAccounts(ctx context.Context, tx pgx.Tx, p *plan) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, len(p.accounts))
	for i, acc := range p.accounts {
		if p.existing[i] != nil {
			ids[i] = p.existing[i].ID
			continue
		}
		created := &account.Account{
			Name: acc.Ref.Name, Institution: acc.Ref.Institution, Kind: acc.Ref.Kind, Currency: acc.Ref.Currency,
			AccountNumber: acc.Ref.AccountNumber, ExternalRef: acc.Ref.ExternalRef,
			OpeningBalanceMinor: openingBalance(acc),
		}
		if err := s.accounts.Create(ctx, tx, created); err != nil {
			return nil, err
		}
		ids[i] = created.ID
		p.result.Accounts[i].AccountID = &created.ID
	}
	return ids, nil
}

// openingBalance is known only when the statement carries running balances: the
// balance before the first row. Otherwise the user enters it on the account.
func openingBalance(acc *statement.Account) int64 {
	first := acc.Rows[0]
	if first.BalanceAfterMinor == nil {
		return 0
	}
	return *first.BalanceAfterMinor - first.AmountMinor
}

func insertTransactions(ctx context.Context, tx pgx.Tx, p *plan, accountIDs []uuid.UUID, batchID uuid.UUID) ([]categorization.Candidate, error) {
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE import_staging (LIKE transaction INCLUDING DEFAULTS) ON COMMIT DROP`); err != nil {
		return nil, eris.Wrap(err, "create staging table")
	}
	columns := []string{"id", "account_id", "booked_on", "amount_minor", "currency", "fee_minor", "description",
		"counterparty", "counterparty_account", "operation_type", "message", "variable_symbol", "constant_symbol",
		"specific_symbol", "bank_category", "balance_after_minor", "import_batch_id", "source_line"}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"import_staging"}, columns, pgx.CopyFromSlice(len(p.rows), func(i int) ([]any, error) {
		r := p.rows[i]
		row := r.row
		return []any{r.id, accountIDs[r.account], row.BookedOn, row.AmountMinor, string(p.accounts[r.account].Ref.Currency),
			row.FeeMinor, row.Description, nullable(row.Counterparty), nullable(row.CounterpartyAccount),
			nullable(row.OperationType), nullable(row.Message), nullable(row.VariableSymbol), nullable(row.ConstantSymbol),
			nullable(row.SpecificSymbol), nullable(row.BankCategory), row.BalanceAfterMinor, batchID, row.Line}, nil
	}))
	if err != nil {
		return nil, eris.Wrap(err, "copy transactions")
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO transaction SELECT * FROM import_staging ON CONFLICT (id) DO NOTHING
		RETURNING id, amount_minor, description, coalesce(counterparty, ''), coalesce(counterparty_account, ''),
			coalesce(operation_type, ''), coalesce(variable_symbol, ''), coalesce(bank_category, '')`)
	if err != nil {
		return nil, eris.Wrap(err, "insert transactions")
	}
	inserted, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (categorization.Candidate, error) {
		var c categorization.Candidate
		err := row.Scan(&c.TransactionID, &c.AmountMinor, &c.Description, &c.Counterparty, &c.CounterpartyAccount,
			&c.OperationType, &c.VariableSymbol, &c.BankCategory)
		return c, err
	})
	return inserted, eris.Wrap(err, "read inserted transactions")
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type Batch struct {
	ID            uuid.UUID
	FileName      string
	Format        string
	PeriodFrom    *time.Time
	PeriodTo      *time.Time
	Status        string
	Forced        bool
	RowsTotal     int
	RowsInserted  int
	RowsDuplicate int
	CreatedAt     time.Time
	RolledBackAt  *time.Time
}

func (s *Service) List(ctx context.Context) ([]Batch, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, file_name, format, period_from, period_to, status, forced, rows_total, rows_inserted, rows_duplicate,
			created_at, rolled_back_at
		FROM import_batch ORDER BY created_at DESC`)
	if err != nil {
		return nil, eris.Wrap(err, "list imports")
	}
	batches, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Batch, error) {
		var b Batch
		err := row.Scan(&b.ID, &b.FileName, &b.Format, &b.PeriodFrom, &b.PeriodTo, &b.Status, &b.Forced, &b.RowsTotal,
			&b.RowsInserted, &b.RowsDuplicate, &b.CreatedAt, &b.RolledBackAt)
		return b, err
	})
	return batches, eris.Wrap(err, "read imports")
}

// Rollback removes every transaction the import created, with their categories
// and transfer pairs (F-IMP-4). The batch row stays as history.
func (s *Service) Rollback(ctx context.Context, id uuid.UUID) (int, error) {
	var removed int64
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM import_batch WHERE id = $1 FOR UPDATE`, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBatchNotFound
		}
		if err != nil {
			return eris.Wrap(err, "lock import")
		}
		if status == "rolled_back" {
			return ErrAlreadyRolledBack
		}
		tag, err := tx.Exec(ctx, `DELETE FROM transaction WHERE import_batch_id = $1`, id)
		if err != nil {
			return eris.Wrap(err, "delete imported transactions")
		}
		removed = tag.RowsAffected()
		_, err = tx.Exec(ctx, `UPDATE import_batch SET status = 'rolled_back', rolled_back_at = now() WHERE id = $1`, id)
		return eris.Wrap(err, "mark import rolled back")
	})
	return int(removed), err
}
