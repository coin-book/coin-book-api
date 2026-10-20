package categorization

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

var ErrTransactionNotFound = eris.New("categorization: transaction not found")

const (
	defaultRulePriority = 100
	quickPickLimit      = 9
)

type Service struct {
	pool       *db.Pool
	repo       *Repo
	thresholds Thresholds
	learner    learner
}

func NewService(pool *db.Pool, repo *Repo, thresholds Thresholds) *Service {
	return &Service{pool: pool, repo: repo, thresholds: thresholds}
}

type AutoStats struct {
	ByFile  int
	ByRule  int
	ByBank  int
	ByModel int
}

// hinted resolves a category the file named itself; the user chose it, so it wins over rules.
func hinted(hint string, byCode, byName map[string]uuid.UUID) (uuid.UUID, bool) {
	if hint == "" {
		return uuid.Nil, false
	}
	if id, ok := byCode[hint]; ok {
		return id, true
	}
	id, ok := byName[normalize(hint)]
	return id, ok
}

// CategorizeNew assigns freshly imported transactions by rule, falling back to the
// bank's own category. Runs inside the import transaction.
func (s *Service) CategorizeNew(ctx context.Context, q db.Querier, candidates []Candidate) (AutoStats, error) {
	rules, err := s.repo.Rules(ctx, q)
	if err != nil {
		return AutoStats{}, err
	}
	cats, err := s.repo.Categories(ctx, q)
	if err != nil {
		return AutoStats{}, err
	}
	byCode := make(map[string]uuid.UUID, len(cats))
	byName := make(map[string]uuid.UUID, 3*len(cats))
	for _, c := range cats {
		byCode[c.Code] = c.ID
		byName[normalize(c.NameCS)] = c.ID
		byName[normalize(c.NameSK)] = c.ID
		byName[normalize(c.NameEN)] = c.ID
	}

	threshold, err := s.thresholds.Threshold(ctx, q)
	if err != nil {
		return AutoStats{}, err
	}
	var model *Classifier
	if threshold > 0 {
		if model, err = s.Model(ctx); err != nil {
			return AutoStats{}, err
		}
	}

	matcher := NewMatcher(rules)
	var stats AutoStats
	assignments := make([]newAssignment, 0, len(candidates))
	for _, c := range candidates {
		if id, ok := hinted(c.CategoryHint, byCode, byName); ok {
			assignments = append(assignments, newAssignment{c.TransactionID, id, c.AmountMinor, SourceManual, nil})
			stats.ByFile++
			continue
		}
		if rule := matcher.Match(c); rule != nil {
			ruleID := rule.ID
			assignments = append(assignments, newAssignment{c.TransactionID, rule.CategoryID, c.AmountMinor, SourceRule, &ruleID})
			stats.ByRule++
			continue
		}
		if id, ok := byCode[bankCategoryHints[c.BankCategory]]; ok {
			assignments = append(assignments, newAssignment{c.TransactionID, id, c.AmountMinor, SourceBank, nil})
			stats.ByBank++
			continue
		}
		if model == nil {
			continue
		}
		if p := model.Predict(c, 1); len(p) == 1 && p[0].Confidence >= threshold {
			assignments = append(assignments, newAssignment{c.TransactionID, p[0].CategoryID, c.AmountMinor, SourceModel, nil})
			stats.ByModel++
		}
	}
	if len(assignments) == 0 {
		return stats, nil
	}
	return stats, s.repo.insertAssignments(ctx, q, assignments)
}

type RuleMatch string

const (
	MatchByCounterparty        RuleMatch = "counterparty"
	MatchByDescription         RuleMatch = "description"
	MatchByCounterpartyAccount RuleMatch = "counterparty_account"
)

type AssignResult struct {
	Rule           *Rule
	AppliedToOther int
	// SuggestedRule is offered when no rule was saved but the choice keeps repeating.
	SuggestedRule *RuleSuggestion
}

// Assign records a manual categorisation, optionally turning it into a rule that
// is applied straight away to every transaction not categorised by hand.
func (s *Service) Assign(ctx context.Context, transactionID string, lines []Line, saveRule *RuleMatch) (*AssignResult, error) {
	result := &AssignResult{}
	err := db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := s.repo.candidate(ctx, tx, transactionID)
		if err != nil {
			return err
		}
		if err := validateLines(c.AmountMinor, lines); err != nil {
			return err
		}
		if err := s.replace(ctx, tx, transactionID, lines); err != nil {
			return err
		}
		if len(lines) != 1 {
			return nil
		}
		if saveRule == nil {
			result.SuggestedRule, err = s.ruleSuggestion(ctx, tx, *c, lines[0].CategoryID)
			return err
		}
		rule, err := ruleFrom(*c, lines[0].CategoryID, *saveRule)
		if err != nil {
			return err
		}
		if err := s.repo.CreateRule(ctx, tx, rule); err != nil {
			return err
		}
		result.Rule = rule
		result.AppliedToOther, err = s.applyRule(ctx, tx, *rule, transactionID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) replace(ctx context.Context, q db.Querier, transactionID string, lines []Line) error {
	if err := s.repo.deleteAssignments(ctx, q, []string{transactionID}, []Source{SourceRule, SourceManual, SourceBank, SourceModel, SourceLoan}); err != nil {
		return err
	}
	assignments := make([]newAssignment, len(lines))
	for i, l := range lines {
		assignments[i] = newAssignment{transactionID, l.CategoryID, l.AmountMinor, SourceManual, nil}
	}
	return mapMissingCategory(s.repo.insertAssignments(ctx, q, assignments))
}

func (s *Service) applyRule(ctx context.Context, q db.Querier, rule Rule, except string) (int, error) {
	candidates, err := s.repo.reassignable(ctx, q)
	if err != nil {
		return 0, err
	}
	matcher := NewMatcher([]Rule{rule})
	var ids []string
	var assignments []newAssignment
	for _, c := range candidates {
		if c.TransactionID == except || matcher.Match(c) == nil {
			continue
		}
		ruleID := rule.ID
		ids = append(ids, c.TransactionID)
		assignments = append(assignments, newAssignment{c.TransactionID, rule.CategoryID, c.AmountMinor, SourceRule, &ruleID})
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.repo.deleteAssignments(ctx, q, ids, []Source{SourceBank, SourceModel}); err != nil {
		return 0, err
	}
	return len(ids), s.repo.insertAssignments(ctx, q, assignments)
}

func ruleFrom(c Candidate, categoryID uuid.UUID, match RuleMatch) (*Rule, error) {
	rule := &Rule{CategoryID: categoryID, Priority: defaultRulePriority}
	value := ""
	switch match {
	case MatchByCounterparty:
		value = c.Counterparty
		rule.MatchCounterparty = &value
	case MatchByDescription:
		value = c.Description
		rule.MatchDescription = &value
	case MatchByCounterpartyAccount:
		value = c.CounterpartyAccount
		rule.MatchCounterpartyAccount = &value
	}
	if value == "" {
		return nil, ErrNothingToMatch
	}
	return rule, nil
}

func validateLines(amount int64, lines []Line) error {
	if len(lines) == 0 {
		return ErrNoLines
	}
	var sum int64
	for _, l := range lines {
		sum += l.AmountMinor
	}
	if sum != amount {
		return ErrSplitMismatch
	}
	return nil
}

type Triage struct {
	TransactionID string
	Remaining     int
	Suggested     []Prediction
	QuickPicks    []Suggestion
}

// NextForTriage returns focus if it is still uncategorised, else the newest uncategorised
// transaction not in skip, with category suggestions; an empty TransactionID means nothing
// is left to show.
func (s *Service) NextForTriage(ctx context.Context, remaining int, skip []string, focus string) (*Triage, error) {
	if skip == nil {
		skip = []string{}
	}
	id, err := s.repo.nextUncategorized(ctx, skip, focus)
	if err != nil || id == "" {
		return &Triage{Remaining: remaining}, err
	}
	c, err := s.repo.candidate(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	t := &Triage{TransactionID: id, Remaining: remaining}
	model, err := s.Model(ctx)
	if err != nil {
		return nil, err
	}
	t.Suggested = model.Predict(*c, suggestionCount)
	t.QuickPicks, err = s.repo.recentlyUsed(ctx, quickPickLimit)
	return t, err
}

// ClearAutomatic removes rule and bank-hint categories from transactions that turned
// out to be transfers, which are outside income and expenses (F-PRE-3).
func (s *Service) ClearAutomatic(ctx context.Context, q db.Querier, transactionIDs []string) error {
	if len(transactionIDs) == 0 {
		return nil
	}
	return s.repo.deleteAssignments(ctx, q, transactionIDs, []Source{SourceRule, SourceBank, SourceModel})
}

func mapMissingCategory(err error) error {
	if db.IsForeignKeyViolation(err) {
		return ErrCategoryNotFound
	}
	return err
}
