// Package transfer pairs movements between the user's own accounts (F-PRE).
package transfer

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

const MatchWindowDays = 3

const exchangeOperation = "exchange"

type MatchedBy string

const (
	Auto   MatchedBy = "auto"
	Manual MatchedBy = "manual"
)

var (
	ErrNotPairable = eris.New("transfer: these two transactions cannot form a transfer")
	ErrNotPaired   = eris.New("transfer: the transaction is not part of a transfer")
)

type Pair struct {
	OutTxID string
	InTxID  string
}

type Candidate struct {
	TransactionID string
	CandidateIDs  []string
	// NumberMatches are the candidates whose account number one leg names as its counterparty.
	NumberMatches []string
}

// Categorizer drops automatic categories from transactions once they are known transfers.
type Categorizer interface {
	ClearAutomatic(ctx context.Context, q db.Querier, transactionIDs []string) error
}

// LoanReconciler turns one-leg transfers to a loan into its instalments (ADR 211).
type LoanReconciler interface {
	Reconcile(ctx context.Context, q db.Querier) error
}

type Service struct {
	pool        *db.Pool
	categorizer Categorizer
	loans       LoanReconciler
}

// NewService takes a nil loans when transfers to loans need not become instalments.
func NewService(pool *db.Pool, categorizer Categorizer, loans LoanReconciler) *Service {
	return &Service{pool: pool, categorizer: categorizer, loans: loans}
}

// candidateSQL pairs each transaction free to pair ($1 when filtered) with opposite movements in
// another own account: same amount within the window, or an exchange leg the same day. Each arm
// joins on an indexed equality; an OR of the two compared every pair of rows.
func candidateSQL(filtered bool) string {
	filter := "$1::text[] IS NULL AND "
	if filtered {
		filter = "t.id = ANY($1) AND "
	}
	return `
	WITH n AS (
		SELECT t.id, t.account_id, t.amount_minor, t.currency, t.booked_on, t.operation_type, t.counterparty_account
		FROM transaction t
		WHERE ` + filter + `t.amount_minor <> 0 AND ` + freeToPair("t") + `
	), pairs AS (
		SELECT n.id AS n_id, c.id AS c_id FROM n JOIN transaction c
			ON c.currency = n.currency AND c.amount_minor = -n.amount_minor
			AND c.booked_on BETWEEN n.booked_on - $2::int AND n.booked_on + $2::int AND c.account_id <> n.account_id
		UNION ALL
		SELECT n.id, c.id FROM n JOIN transaction c
			ON c.booked_on = n.booked_on AND c.operation_type = '` + exchangeOperation + `'
			AND c.currency <> n.currency AND sign(c.amount_minor) = -sign(n.amount_minor) AND c.account_id <> n.account_id
		WHERE n.operation_type = '` + exchangeOperation + `'
	)
	SELECT n.id, n.amount_minor, c.id,
		coalesce(n.counterparty_account = ca.account_number, false)
			OR coalesce(c.counterparty_account = na.account_number, false) AS strong,
		(SELECT count(*) FROM transaction x
			WHERE x.currency = c.currency AND x.amount_minor = -c.amount_minor AND x.account_id <> c.account_id
			  AND x.booked_on BETWEEN c.booked_on - $2::int AND c.booked_on + $2::int
			  AND NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.out_tx_id = x.id)
			  AND NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.in_tx_id = x.id)) AS rivals
	FROM pairs p
	JOIN n ON n.id = p.n_id
	JOIN transaction c ON c.id = p.c_id
	JOIN account na ON na.id = n.account_id
	JOIN account ca ON ca.id = c.account_id
	WHERE ` + freeToPair("c") + `
	ORDER BY n.booked_on, n.id, c.booked_on, c.id`
}

// freeToPair: the transaction is in no pair and pays no loan instalment.
func freeToPair(alias string) string {
	return `NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.out_tx_id = ` + alias + `.id)
		AND NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.in_tx_id = ` + alias + `.id)
		AND NOT EXISTS (SELECT 1 FROM loan_payment lp WHERE lp.transaction_id = ` + alias + `.id)`
}

type edge struct {
	from   string
	amount int64
	to     string
	strong bool
	rivals int
}

func loadEdges(ctx context.Context, q db.Querier, ids []string) ([]edge, error) {
	rows, err := q.Query(ctx, candidateSQL(ids != nil), ids, MatchWindowDays)
	if err != nil {
		return nil, eris.Wrap(err, "find transfer candidates")
	}
	edges, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (edge, error) {
		var e edge
		err := row.Scan(&e.from, &e.amount, &e.to, &e.strong, &e.rivals)
		return e, err
	})
	return edges, eris.Wrap(err, "read transfer candidates")
}

// AutoMatch pairs the given new transactions where the match is unambiguous: a
// single candidate that has no rival, or a single candidate whose account the
// payment names. Anything else is left for the user (F-PRE-2).
func (s *Service) AutoMatch(ctx context.Context, q db.Querier, transactionIDs []string) (int, error) {
	if len(transactionIDs) == 0 {
		return 0, nil
	}
	edges, err := loadEdges(ctx, q, transactionIDs)
	if err != nil {
		return 0, err
	}
	used := map[string]bool{}
	var pairs []Pair
	for _, group := range groupByFrom(edges) {
		chosen := pick(group)
		if chosen == nil || used[chosen.from] || used[chosen.to] {
			continue
		}
		used[chosen.from], used[chosen.to] = true, true
		pairs = append(pairs, orient(chosen.from, chosen.amount, chosen.to))
	}
	if len(pairs) == 0 {
		return 0, nil
	}
	return len(pairs), s.insert(ctx, q, pairs, Auto)
}

func pick(group []edge) *edge {
	var strong []edge
	for _, e := range group {
		if e.strong {
			strong = append(strong, e)
		}
	}
	if len(strong) == 1 {
		return &strong[0]
	}
	if len(group) == 1 && group[0].rivals <= 1 {
		return &group[0]
	}
	return nil
}

func groupByFrom(edges []edge) [][]edge {
	var groups [][]edge
	for i, e := range edges {
		if i == 0 || edges[i-1].from != e.from {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], e)
	}
	return groups
}

func orient(a string, amountA int64, b string) Pair {
	if amountA < 0 {
		return Pair{OutTxID: a, InTxID: b}
	}
	return Pair{OutTxID: b, InTxID: a}
}

func (s *Service) insert(ctx context.Context, q db.Querier, pairs []Pair, by MatchedBy) error {
	ids := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		if _, err := q.Exec(ctx, `INSERT INTO transfer_pair (out_tx_id, in_tx_id, matched_by) VALUES ($1, $2, $3)`,
			p.OutTxID, p.InTxID, string(by)); err != nil {
			return eris.Wrap(err, "insert transfer pair")
		}
		ids = append(ids, p.OutTxID, p.InTxID)
	}
	if _, err := q.Exec(ctx, `DELETE FROM own_transfer WHERE transaction_id = ANY($1)`, ids); err != nil {
		return eris.Wrap(err, "replace one-leg transfers with the pair")
	}
	return s.categorizer.ClearAutomatic(ctx, q, ids)
}

// Ambiguous lists unpaired transactions that have more than one plausible counterpart.
func (s *Service) Ambiguous(ctx context.Context) ([]Candidate, error) {
	edges, err := loadEdges(ctx, s.pool, nil)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, group := range groupByFrom(edges) {
		if pick(group) != nil || group[0].amount > 0 {
			continue
		}
		c := Candidate{TransactionID: group[0].from}
		for _, e := range group {
			c.CandidateIDs = append(c.CandidateIDs, e.to)
			if e.strong {
				c.NumberMatches = append(c.NumberMatches, e.to)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *Service) Pair(ctx context.Context, outTxID, inTxID string) error {
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		err := tx.QueryRow(ctx, `
			SELECT o.amount_minor < 0 AND i.amount_minor > 0 AND o.account_id <> i.account_id
				AND NOT EXISTS (SELECT 1 FROM transfer_pair tp WHERE tp.out_tx_id IN (o.id, i.id) OR tp.in_tx_id IN (o.id, i.id))
				AND NOT EXISTS (SELECT 1 FROM loan_payment lp WHERE lp.transaction_id IN (o.id, i.id))
			FROM transaction o, transaction i WHERE o.id = $1 AND i.id = $2`, outTxID, inTxID).Scan(&ok)
		if err != nil || !ok {
			return ErrNotPairable
		}
		return s.insert(ctx, tx, []Pair{{OutTxID: outTxID, InTxID: inTxID}}, Manual)
	})
}

type Listed struct {
	OutTxID   string
	InTxID    string
	MatchedBy MatchedBy
	CreatedAt time.Time
}

func (s *Service) List(ctx context.Context, limit int) ([]Listed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tp.out_tx_id, tp.in_tx_id, tp.matched_by, tp.created_at
		FROM transfer_pair tp JOIN transaction o ON o.id = tp.out_tx_id
		ORDER BY o.booked_on DESC LIMIT $1`, limit)
	if err != nil {
		return nil, eris.Wrap(err, "list transfers")
	}
	pairs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Listed, error) {
		var l Listed
		err := row.Scan(&l.OutTxID, &l.InTxID, &l.MatchedBy, &l.CreatedAt)
		return l, err
	})
	return pairs, eris.Wrap(err, "read transfers")
}
