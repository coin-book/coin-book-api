package categorization

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// or picks the caller's transaction, or the pool when the call stands on its own.
func (r *Repo) or(q db.Querier) db.Querier {
	if q == nil {
		return r.pool
	}
	return q
}

const categoryColumns = `id, code, parent_id, name_cs, name_sk, kind, color_key, icon, budget_group_id, sort, custom`

func scanCategory(row pgx.CollectableRow) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.Code, &c.ParentID, &c.NameCS, &c.NameSK, &c.Kind, &c.ColorKey, &c.Icon, &c.BudgetGroupID, &c.Sort, &c.Custom)
	return c, err
}

func (r *Repo) Categories(ctx context.Context, q db.Querier) ([]Category, error) {
	rows, err := r.or(q).Query(ctx, `SELECT `+categoryColumns+` FROM any_category ORDER BY parent_id NULLS FIRST, sort, code`)
	if err != nil {
		return nil, eris.Wrap(err, "list categories")
	}
	cats, err := pgx.CollectRows(rows, scanCategory)
	return cats, eris.Wrap(err, "read categories")
}

func (r *Repo) BudgetGroups(ctx context.Context) ([]BudgetGroup, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, code, name_cs, name_sk, sort FROM budget_group ORDER BY sort`)
	if err != nil {
		return nil, eris.Wrap(err, "list budget groups")
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (BudgetGroup, error) {
		var g BudgetGroup
		err := row.Scan(&g.ID, &g.Code, &g.NameCS, &g.NameSK, &g.Sort)
		return g, err
	})
	return groups, eris.Wrap(err, "read budget groups")
}

const ruleColumns = `id, category_id, priority, match_counterparty, match_description, match_counterparty_account,
	match_operation_type, match_variable_symbol, created_at`

func scanRule(row pgx.CollectableRow) (Rule, error) {
	var r Rule
	err := row.Scan(&r.ID, &r.CategoryID, &r.Priority, &r.MatchCounterparty, &r.MatchDescription,
		&r.MatchCounterpartyAccount, &r.MatchOperationType, &r.MatchVariableSymbol, &r.CreatedAt)
	return r, err
}

func (r *Repo) Rules(ctx context.Context, q db.Querier) ([]Rule, error) {
	rows, err := r.or(q).Query(ctx, `SELECT `+ruleColumns+` FROM rule ORDER BY priority, created_at`)
	if err != nil {
		return nil, eris.Wrap(err, "list rules")
	}
	rules, err := pgx.CollectRows(rows, scanRule)
	return rules, eris.Wrap(err, "read rules")
}

func (r *Repo) CreateRule(ctx context.Context, q db.Querier, rule *Rule) error {
	err := r.or(q).QueryRow(ctx, `
		INSERT INTO rule (system_category_id, user_category_id, priority, match_counterparty, match_description,
			match_counterparty_account, match_operation_type, match_variable_symbol)
		SELECT (SELECT id FROM category WHERE id = $1), (SELECT id FROM user_category WHERE id = $1),
			$2, $3, $4, $5, $6, $7
		RETURNING id, created_at`,
		rule.CategoryID, rule.Priority, rule.MatchCounterparty, rule.MatchDescription, rule.MatchCounterpartyAccount,
		rule.MatchOperationType, rule.MatchVariableSymbol).Scan(&rule.ID, &rule.CreatedAt)
	return eris.Wrap(err, "create rule")
}

func (r *Repo) DeleteRule(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM rule WHERE id = $1`, id)
	if err != nil {
		return eris.Wrap(err, "delete rule")
	}
	if tag.RowsAffected() == 0 {
		return ErrRuleNotFound
	}
	return nil
}

type newAssignment struct {
	transactionID string
	categoryID    uuid.UUID
	amountMinor   int64
	source        Source
	ruleID        *uuid.UUID
}

func (r *Repo) insertAssignments(ctx context.Context, q db.Querier, assignments []newAssignment) error {
	txIDs := make([]string, len(assignments))
	categoryIDs := make([]uuid.UUID, len(assignments))
	amounts := make([]int64, len(assignments))
	sources := make([]string, len(assignments))
	ruleIDs := make([]*uuid.UUID, len(assignments))
	for i, a := range assignments {
		txIDs[i], categoryIDs[i], amounts[i], sources[i], ruleIDs[i] = a.transactionID, a.categoryID, a.amountMinor, string(a.source), a.ruleID
	}
	_, err := q.Exec(ctx, `
		INSERT INTO category_assignment (transaction_id, system_category_id, user_category_id, amount_minor, source, rule_id)
		SELECT a.tx, c.id, u.id, a.amount, a.source, a.rule
		FROM unnest($1::text[], $2::uuid[], $3::bigint[], $4::text[], $5::uuid[]) AS a (tx, category, amount, source, rule)
		LEFT JOIN category c ON c.id = a.category LEFT JOIN user_category u ON u.id = a.category`,
		txIDs, categoryIDs, amounts, sources, ruleIDs)
	return eris.Wrap(err, "insert category assignments")
}

func (r *Repo) deleteAssignments(ctx context.Context, q db.Querier, transactionIDs []string, sources []Source) error {
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = string(s)
	}
	_, err := q.Exec(ctx, `DELETE FROM category_assignment WHERE transaction_id = ANY($1) AND source = ANY($2)`,
		transactionIDs, names)
	return eris.Wrap(err, "delete category assignments")
}

const candidateColumns = `t.id, t.amount_minor, t.description, coalesce(t.counterparty, ''), coalesce(t.counterparty_account, ''),
	coalesce(t.operation_type, ''), coalesce(t.variable_symbol, ''), coalesce(t.bank_category, '')`

func scanCandidate(row pgx.CollectableRow) (Candidate, error) {
	var c Candidate
	err := row.Scan(&c.TransactionID, &c.AmountMinor, &c.Description, &c.Counterparty, &c.CounterpartyAccount,
		&c.OperationType, &c.VariableSymbol, &c.BankCategory)
	return c, err
}

func (r *Repo) candidate(ctx context.Context, q db.Querier, transactionID string) (*Candidate, error) {
	rows, err := q.Query(ctx, `SELECT `+candidateColumns+` FROM transaction t WHERE t.id = $1`, transactionID)
	if err != nil {
		return nil, eris.Wrap(err, "get transaction")
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCandidate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTransactionNotFound
	}
	return &c, eris.Wrap(err, "read transaction")
}

func (r *Repo) candidates(ctx context.Context, q db.Querier, ids []string) ([]Candidate, error) {
	rows, err := q.Query(ctx, `SELECT `+candidateColumns+` FROM transaction t WHERE t.id = ANY($1)`, ids)
	if err != nil {
		return nil, eris.Wrap(err, "list transactions")
	}
	cands, err := pgx.CollectRows(rows, scanCandidate)
	return cands, eris.Wrap(err, "read transactions")
}

// reassignable lists transactions a new rule may claim: nothing assigned yet, or
// only a bank hint. A manual choice is never overwritten by a rule.
func (r *Repo) reassignable(ctx context.Context, q db.Querier) ([]Candidate, error) {
	rows, err := q.Query(ctx, `
		SELECT `+candidateColumns+` FROM transaction t
		WHERE NOT EXISTS (SELECT 1 FROM category_assignment ca WHERE ca.transaction_id = t.id AND ca.source NOT IN ('bank', 'model'))
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)`)
	if err != nil {
		return nil, eris.Wrap(err, "list reassignable transactions")
	}
	cands, err := pgx.CollectRows(rows, scanCandidate)
	return cands, eris.Wrap(err, "read reassignable transactions")
}

type Suggestion struct {
	CategoryID uuid.UUID
	Uses       int
}

func (r *Repo) recentlyUsed(ctx context.Context, limit int) ([]Suggestion, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT category_id, count(*) FROM (
			SELECT category_id FROM category_assignment WHERE source = 'manual' ORDER BY assigned_at DESC LIMIT 200
		) recent GROUP BY category_id ORDER BY count(*) DESC LIMIT $1`, limit)
	if err != nil {
		return nil, eris.Wrap(err, "recently used categories")
	}
	return collectSuggestions(rows)
}

func collectSuggestions(rows pgx.Rows) ([]Suggestion, error) {
	s, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Suggestion, error) {
		var s Suggestion
		err := row.Scan(&s.CategoryID, &s.Uses)
		return s, err
	})
	return s, eris.Wrap(err, "read suggestions")
}

func (r *Repo) nextUncategorized(ctx context.Context, skip []string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		SELECT t.id FROM transaction t
		WHERE NOT EXISTS (SELECT 1 FROM category_assignment ca WHERE ca.transaction_id = t.id)
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)
		  AND NOT (t.id = ANY($1))
		ORDER BY t.booked_on DESC, t.id LIMIT 1`, skip).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, eris.Wrap(err, "next uncategorized")
}
