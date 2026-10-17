package categorization

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

const (
	suggestionCount = 3
	// ruleSuggestionMinimum: after this many hand-made choices of one category for one
	// counterparty, offer to turn them into a rule.
	ruleSuggestionMinimum = 3
)

// Thresholds reads the auto-categorisation threshold; 0 disables it.
type Thresholds interface {
	Threshold(ctx context.Context, q db.Querier) (float64, error)
}

// learner keeps the trained classifier until the categorised data changes.
type learner struct {
	mu          sync.Mutex
	model       *Classifier
	fingerprint string
}

func (r *Repo) trainingFingerprint(ctx context.Context, q db.Querier) (string, error) {
	var count int64
	var latest time.Time
	var categories int64
	// Merging categories moves assignments without touching assigned_at; the category count catches it.
	err := q.QueryRow(ctx, `SELECT count(*), coalesce(max(assigned_at), 'epoch'), (SELECT count(*) FROM any_category)
		FROM category_assignment WHERE source <> 'model'`).Scan(&count, &latest, &categories)
	if err != nil {
		return "", eris.Wrap(err, "fingerprint categorised data")
	}
	return latest.Format(time.RFC3339Nano) + "/" + strconv.FormatInt(count, 10) + "/" + strconv.FormatInt(categories, 10), nil
}

// trainingExamples are whole-transaction categories the model may learn from: not
// splits, not transfers, not the model's own guesses.
func (r *Repo) trainingExamples(ctx context.Context, q db.Querier) ([]Example, error) {
	rows, err := q.Query(ctx, `
		SELECT `+candidateColumns+`, ca.category_id, ca.source
		FROM transaction t JOIN category_assignment ca ON ca.transaction_id = t.id
		WHERE ca.source IN ('manual', 'rule', 'bank')
		  AND NOT EXISTS (SELECT 1 FROM category_assignment o WHERE o.transaction_id = t.id AND o.category_id <> ca.category_id)
		  AND NOT EXISTS (SELECT 1 FROM transfer_leg tl WHERE tl.transaction_id = t.id)`)
	if err != nil {
		return nil, eris.Wrap(err, "list training examples")
	}
	examples, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Example, error) {
		var e Example
		c := &e.Candidate
		err := row.Scan(&c.TransactionID, &c.AmountMinor, &c.Description, &c.Counterparty, &c.CounterpartyAccount,
			&c.OperationType, &c.VariableSymbol, &c.BankCategory, &e.CategoryID, &e.Source)
		return e, err
	})
	return examples, eris.Wrap(err, "read training examples")
}

// Model returns the classifier, retraining it when categories changed since last time.
// Retraining on ten thousand transactions takes milliseconds, so it simply starts over.
func (s *Service) Model(ctx context.Context) (*Classifier, error) {
	fingerprint, err := s.repo.trainingFingerprint(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	s.learner.mu.Lock()
	defer s.learner.mu.Unlock()
	if s.learner.model != nil && s.learner.fingerprint == fingerprint {
		return s.learner.model, nil
	}
	examples, err := s.repo.trainingExamples(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	s.learner.model, s.learner.fingerprint = Train(examples), fingerprint
	return s.learner.model, nil
}

// Suggest predicts categories for the given transactions, keyed by id.
func (s *Service) Suggest(ctx context.Context, transactionIDs []string) (map[string][]Prediction, error) {
	out := map[string][]Prediction{}
	if len(transactionIDs) == 0 {
		return out, nil
	}
	model, err := s.Model(ctx)
	if err != nil {
		return nil, err
	}
	candidates, err := s.repo.candidates(ctx, s.pool, transactionIDs)
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if p := model.Predict(c, suggestionCount); len(p) > 0 {
			out[c.TransactionID] = p
		}
	}
	return out, nil
}

// Confirm turns automatic categories into the user's own, for the given transactions
// or, with nil, for all of them.
func (s *Service) Confirm(ctx context.Context, transactionIDs []string) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE category_assignment SET source = 'manual', assigned_at = now()
		WHERE source = 'model' AND ($1::text[] IS NULL OR transaction_id = ANY($1))`, transactionIDs)
	return int(tag.RowsAffected()), eris.Wrap(err, "confirm automatic categories")
}

type RuleSuggestion struct {
	Counterparty string
	CategoryID   uuid.UUID
	Times        int
}

// ruleSuggestion offers a rule once the user has put the same counterparty into the
// same category by hand often enough and no rule covers it yet.
func (s *Service) ruleSuggestion(ctx context.Context, q db.Querier, c Candidate, categoryID uuid.UUID) (*RuleSuggestion, error) {
	if c.Counterparty == "" {
		return nil, nil
	}
	rules, err := s.repo.Rules(ctx, q)
	if err != nil {
		return nil, err
	}
	if NewMatcher(rules).Match(c) != nil {
		return nil, nil
	}
	var times int
	err = q.QueryRow(ctx, `SELECT count(*) FROM category_assignment ca JOIN transaction t ON t.id = ca.transaction_id
		WHERE ca.source = 'manual' AND ca.category_id = $1 AND lower(t.counterparty) = lower($2)`, categoryID, c.Counterparty).Scan(&times)
	if err != nil {
		return nil, eris.Wrap(err, "count manual choices")
	}
	if times < ruleSuggestionMinimum {
		return nil, nil
	}
	return &RuleSuggestion{Counterparty: c.Counterparty, CategoryID: categoryID, Times: times}, nil
}
