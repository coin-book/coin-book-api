package budget

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/spending"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
)

var (
	ErrInvalidLine   = eris.New("budget: a line needs exactly one of category or budget group, and a non-negative amount")
	ErrDuplicate     = eris.New("budget: the same category or group is budgeted twice")
	ErrUnknownTarget = eris.New("budget: unknown category or budget group")
)

type Status string

const (
	OnTrack Status = "on_track"
	// AheadOfPace means the month is not over yet but spending at the current rate
	// would exceed the plan by month end (F-VYU-1 early warning).
	AheadOfPace Status = "ahead_of_pace"
	Over        Status = "over"
)

type Line struct {
	CategoryID    *uuid.UUID
	BudgetGroupID *uuid.UUID
	PlannedMinor  int64
}

type LineView struct {
	Line
	SpentMinor     int64
	RemainingMinor int64
	ProjectedMinor int64
	Status         Status
}

type Unbudgeted struct {
	CategoryID uuid.UUID
	SpentMinor int64
}

type Month struct {
	Month          time.Time
	Currency       money.Currency
	Lines          []LineView
	Unbudgeted     []Unbudgeted
	PlannedMinor   int64
	SpentMinor     int64
	RemainingMinor int64
	// Elapsed is the share of the month already gone: 0 for a future month, 1 for a past one.
	Elapsed     float64
	DaysLeft    int
	PerDayMinor int64
}

type Service struct {
	pool       *db.Pool
	spending   *spending.Repo
	categories *categorization.Repo
	clock      clock.Clock
}

func NewService(pool *db.Pool, spendingRepo *spending.Repo, categories *categorization.Repo, c clock.Clock) *Service {
	return &Service{pool: pool, spending: spendingRepo, categories: categories, clock: c}
}

func (s *Service) Get(ctx context.Context, month time.Time, currency money.Currency) (*Month, error) {
	month = spending.MonthStart(month)
	lines, err := s.lines(ctx, s.pool, month, currency)
	if err != nil {
		return nil, err
	}
	totals, err := s.spending.Totals(ctx, month, month.AddDate(0, 1, 0), currency)
	if err != nil {
		return nil, err
	}
	cats, err := s.categories.Categories(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	groups, err := s.categories.BudgetGroups(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(month, currency, lines, totals, cats, lumpyGroups(groups)), nil
}

// fixedGroupCode names the budget group of bills paid once a month (rent, energy):
// spending there arrives in one lump, so a daily pace says nothing about it.
const fixedGroupCode = "fixed"

func lumpyGroups(groups []categorization.BudgetGroup) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, g := range groups {
		if g.Code == fixedGroupCode {
			out[g.ID] = true
		}
	}
	return out
}

func (s *Service) view(month time.Time, currency money.Currency, lines []Line, totals []spending.Total,
	cats []categorization.Category, lumpy map[uuid.UUID]bool) *Month {
	groupOf := map[uuid.UUID]*uuid.UUID{}
	for _, c := range cats {
		if c.ParentID == nil {
			groupOf[c.ID] = c.BudgetGroupID
		}
	}
	spentByCategory := map[uuid.UUID]int64{}
	spentByMain := map[uuid.UUID]int64{}
	spentByGroup := map[uuid.UUID]int64{}
	for _, t := range totals {
		if t.MainCategoryID == nil || (t.Kind != categorization.Expense && t.Kind != categorization.Financial) {
			continue
		}
		spent := -t.AmountMinor
		spentByCategory[*t.CategoryID] += spent
		spentByMain[*t.MainCategoryID] += spent
		if g := groupOf[*t.MainCategoryID]; g != nil {
			spentByGroup[*g] += spent
		}
	}

	m := &Month{Month: month, Currency: currency}
	m.Elapsed, m.DaysLeft = progress(month, clock.Today(s.clock))
	budgeted := map[uuid.UUID]bool{}
	for _, l := range lines {
		var spent int64
		group := l.BudgetGroupID
		if l.CategoryID != nil {
			group = groupOf[mainOf(*l.CategoryID, cats)]
		}
		paced := group == nil || !lumpy[*group]
		switch {
		case l.CategoryID != nil && isMain(*l.CategoryID, groupOf):
			spent = spentByMain[*l.CategoryID]
			budgeted[*l.CategoryID] = true
		case l.CategoryID != nil:
			spent = spentByCategory[*l.CategoryID]
			budgeted[*l.CategoryID] = true
		default:
			spent = spentByGroup[*l.BudgetGroupID]
			for main, g := range groupOf {
				if g != nil && *g == *l.BudgetGroupID {
					budgeted[main] = true
				}
			}
		}
		m.Lines = append(m.Lines, assess(l, spent, m.Elapsed, paced))
		m.PlannedMinor += l.PlannedMinor
		m.SpentMinor += spent
	}
	for main, spent := range spentByMain {
		if !budgeted[main] && spent > 0 {
			m.Unbudgeted = append(m.Unbudgeted, Unbudgeted{CategoryID: main, SpentMinor: spent})
		}
	}
	m.RemainingMinor = m.PlannedMinor - m.SpentMinor
	if m.DaysLeft > 0 && m.RemainingMinor > 0 {
		m.PerDayMinor = m.RemainingMinor / int64(m.DaysLeft)
	}
	return m
}

func isMain(id uuid.UUID, groupOf map[uuid.UUID]*uuid.UUID) bool {
	_, ok := groupOf[id]
	return ok
}

func mainOf(id uuid.UUID, cats []categorization.Category) uuid.UUID {
	for _, c := range cats {
		if c.ID == id && c.ParentID != nil {
			return *c.ParentID
		}
	}
	return id
}

func assess(l Line, spent int64, elapsed float64, paced bool) LineView {
	v := LineView{Line: l, SpentMinor: spent, RemainingMinor: l.PlannedMinor - spent, ProjectedMinor: spent, Status: OnTrack}
	if paced && elapsed > 0 && elapsed < 1 {
		v.ProjectedMinor = int64(float64(spent) / elapsed)
	}
	switch {
	case spent > l.PlannedMinor:
		v.Status = Over
	case v.ProjectedMinor > l.PlannedMinor:
		v.Status = AheadOfPace
	}
	return v
}

// progress counts today as a day still left to spend in.
func progress(month, today time.Time) (float64, int) {
	days := spending.DaysIn(month)
	switch {
	case today.Before(month):
		return 0, days
	case !today.Before(month.AddDate(0, 1, 0)):
		return 1, 0
	default:
		day := today.Day()
		return float64(day-1) / float64(days), days - day + 1
	}
}

func (s *Service) lines(ctx context.Context, q db.Querier, month time.Time, currency money.Currency) ([]Line, error) {
	rows, err := q.Query(ctx, `
		SELECT b.category_id, b.budget_group_id, b.amount_minor FROM budget b
		LEFT JOIN any_category c ON c.id = b.category_id LEFT JOIN budget_group g ON g.id = b.budget_group_id
		WHERE b.month = $1 AND b.currency = $2
		ORDER BY coalesce(g.sort, 0), coalesce(c.sort, 0)`, month, string(currency))
	if err != nil {
		return nil, eris.Wrap(err, "list budget lines")
	}
	lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Line, error) {
		var l Line
		err := row.Scan(&l.CategoryID, &l.BudgetGroupID, &l.PlannedMinor)
		return l, err
	})
	return lines, eris.Wrap(err, "read budget lines")
}

// Replace sets the month's whole plan for one currency.
func (s *Service) Replace(ctx context.Context, month time.Time, currency money.Currency, lines []Line) error {
	month = spending.MonthStart(month)
	seen := map[uuid.UUID]bool{}
	for _, l := range lines {
		if (l.CategoryID == nil) == (l.BudgetGroupID == nil) || l.PlannedMinor < 0 {
			return ErrInvalidLine
		}
		target := l.CategoryID
		if target == nil {
			target = l.BudgetGroupID
		}
		if seen[*target] {
			return ErrDuplicate
		}
		seen[*target] = true
	}
	return db.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM budget WHERE month = $1 AND currency = $2`, month, string(currency)); err != nil {
			return eris.Wrap(err, "clear budget")
		}
		for _, l := range lines {
			_, err := tx.Exec(ctx, `
				INSERT INTO budget (month, system_category_id, user_category_id, budget_group_id, amount_minor, currency)
				SELECT $1, (SELECT id FROM category WHERE id = $2), (SELECT id FROM user_category WHERE id = $2), $3, $4, $5`,
				month, l.CategoryID, l.BudgetGroupID, l.PlannedMinor, string(currency))
			if db.IsForeignKeyViolation(err) || db.IsCheckViolation(err) {
				return ErrUnknownTarget
			}
			if err != nil {
				return eris.Wrap(err, "insert budget line")
			}
		}
		return nil
	})
}

// CopyPrevious fills an empty month with the previous month's plan and reports
// how many lines it copied.
func (s *Service) CopyPrevious(ctx context.Context, month time.Time, currency money.Currency) (int, error) {
	month = spending.MonthStart(month)
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO budget (month, system_category_id, user_category_id, budget_group_id, amount_minor, currency)
		SELECT $1, system_category_id, user_category_id, budget_group_id, amount_minor, currency FROM budget
		WHERE month = $2 AND currency = $3
		  AND NOT EXISTS (SELECT 1 FROM budget WHERE month = $1 AND currency = $3)`,
		month, month.AddDate(0, -1, 0), string(currency))
	return int(tag.RowsAffected()), eris.Wrap(err, "copy previous budget")
}
