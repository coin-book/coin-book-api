package categorization

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"coin-book-api/internal/platform/db"
)

type NewCategory struct {
	// ParentID makes it a subcategory, which takes the parent's kind, colour and budget group.
	ParentID *uuid.UUID
	// Name fills both locales: the user names their own categories once, in their own language.
	Name          string
	Kind          Kind
	ColorKey      *string
	Icon          *string
	BudgetGroupID *uuid.UUID
}

type CategoryUpdate struct {
	Name          *string
	ColorKey      *string
	Icon          *string
	BudgetGroupID *uuid.UUID
	Kind          *Kind
	// ParentID moves a subcategory under another main category.
	ParentID *uuid.UUID
}

type CategoryUsage struct {
	Transactions int
	Rules        int
}

func (r *Repo) category(ctx context.Context, q db.Querier, id uuid.UUID) (*Category, error) {
	rows, err := q.Query(ctx, `SELECT `+categoryColumns+` FROM any_category WHERE id = $1`, id)
	if err != nil {
		return nil, eris.Wrap(err, "load category")
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCategory)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCategoryNotFound
	}
	return &c, eris.Wrap(err, "read category")
}

func (r *Repo) mainCategory(ctx context.Context, q db.Querier, id uuid.UUID) (*Category, error) {
	parent, err := r.category(ctx, q, id)
	if errors.Is(err, ErrCategoryNotFound) {
		return nil, ErrInvalidParent
	}
	if err != nil {
		return nil, err
	}
	if parent.ParentID != nil {
		return nil, ErrInvalidParent
	}
	return parent, nil
}

// splitRef turns one category id into the system and user columns that reference it.
func splitRef(c *Category) (system, user *uuid.UUID) {
	if c.Custom {
		return nil, &c.ID
	}
	return &c.ID, nil
}

func (r *Repo) CreateCategory(ctx context.Context, userID uuid.UUID, in NewCategory) (*Category, error) {
	var created *Category
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		codePrefix := ""
		var parentSystem, parentUser *uuid.UUID
		if in.ParentID != nil {
			parent, err := r.mainCategory(ctx, tx, *in.ParentID)
			if err != nil {
				return err
			}
			in.Kind, in.ColorKey, in.Icon, in.BudgetGroupID = parent.Kind, nil, nil, nil
			codePrefix = parent.Code + "."
			parentSystem, parentUser = splitRef(parent)
		}
		code, err := freeCode(ctx, tx, codePrefix+slug(in.Name))
		if err != nil {
			return err
		}
		var id uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO user_category (user_id, code, parent_category_id, parent_user_category_id, name, kind,
				color_key, icon, budget_group_id, sort)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, coalesce(max(sort), 0) + 10
			FROM user_category WHERE parent_id IS NOT DISTINCT FROM $10
			RETURNING id`,
			userID, code, parentSystem, parentUser, in.Name, string(in.Kind), in.ColorKey, in.Icon, in.BudgetGroupID,
			in.ParentID).Scan(&id)
		if err != nil {
			return eris.Wrap(err, "create category")
		}
		created, err = r.category(ctx, tx, id)
		return err
	})
	return created, err
}

// freeCode keeps a new category's code readable for coin-book CSV hints and unique beside the defaults.
func freeCode(ctx context.Context, q db.Querier, base string) (string, error) {
	rows, err := q.Query(ctx, `SELECT code FROM any_category WHERE code = $1 OR code LIKE $1 || '-%'`, base)
	if err != nil {
		return "", eris.Wrap(err, "list category codes")
	}
	taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", eris.Wrap(err, "read category codes")
	}
	used := make(map[string]bool, len(taken))
	for _, c := range taken {
		used[c] = true
	}
	code := base
	for n := 2; used[code]; n++ {
		code = base + "-" + strconv.Itoa(n)
	}
	return code, nil
}

func slug(name string) string {
	ascii, _, _ := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn))), name)
	words := strings.FieldsFunc(strings.ToLower(ascii), func(r rune) bool {
		return r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r))
	})
	if len(words) == 0 {
		return "category"
	}
	return strings.Join(words, "_")
}

func (r *Repo) UpdateCategory(ctx context.Context, userID, id uuid.UUID, u CategoryUpdate) (*Category, error) {
	var updated *Category
	err := db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		current, err := r.category(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Custom {
			err = updateUserCategory(ctx, tx, r, current, u)
		} else {
			err = setSystemCategoryLook(ctx, tx, userID, current, u)
		}
		if err != nil {
			return err
		}
		updated, err = r.category(ctx, tx, id)
		return err
	})
	return updated, err
}

// setSystemCategoryLook keeps the user's colour, icon and group beside the system row, which never changes.
func setSystemCategoryLook(ctx context.Context, tx pgx.Tx, userID uuid.UUID, c *Category, u CategoryUpdate) error {
	if u.Name != nil || u.Kind != nil || u.ParentID != nil || c.ParentID != nil {
		return ErrSystemCategory
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO category_preference (user_id, category_id, color_key, icon, budget_group_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, category_id) DO UPDATE SET
			color_key = coalesce(EXCLUDED.color_key, category_preference.color_key),
			icon = coalesce(EXCLUDED.icon, category_preference.icon),
			budget_group_id = coalesce(EXCLUDED.budget_group_id, category_preference.budget_group_id)`,
		userID, c.ID, u.ColorKey, u.Icon, u.BudgetGroupID)
	return eris.Wrap(err, "save category look")
}

func updateUserCategory(ctx context.Context, tx pgx.Tx, r *Repo, c *Category, u CategoryUpdate) error {
	var parentSystem, parentUser *uuid.UUID
	if u.ParentID != nil {
		if c.ParentID == nil {
			return ErrInvalidParent
		}
		parent, err := r.mainCategory(ctx, tx, *u.ParentID)
		if err != nil {
			return err
		}
		kind := parent.Kind
		u.Kind = &kind
		parentSystem, parentUser = splitRef(parent)
	}
	if u.Kind != nil && c.ParentID != nil && u.ParentID == nil {
		u.Kind = nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE user_category SET
			name = coalesce($2, name), color_key = coalesce($3, color_key), icon = coalesce($4, icon),
			budget_group_id = coalesce($5, budget_group_id), kind = coalesce($6, kind),
			parent_category_id = CASE WHEN $7 THEN $8 ELSE parent_category_id END,
			parent_user_category_id = CASE WHEN $7 THEN $9 ELSE parent_user_category_id END
		WHERE id = $1`,
		c.ID, u.Name, u.ColorKey, u.Icon, u.BudgetGroupID, (*string)(u.Kind), u.ParentID != nil, parentSystem, parentUser)
	if err != nil {
		return eris.Wrap(err, "update category")
	}
	if u.Kind != nil && c.ParentID == nil {
		_, err = tx.Exec(ctx, `UPDATE user_category SET kind = $2 WHERE parent_user_category_id = $1`, c.ID, string(*u.Kind))
	}
	return eris.Wrap(err, "follow kind in subcategories")
}

// DeleteCategory removes a user category; a nil mergeInto is only allowed when nothing uses it.
// Merging moves its transactions, rules, budgets and subcategories into mergeInto.
func (r *Repo) DeleteCategory(ctx context.Context, id uuid.UUID, mergeInto *uuid.UUID) error {
	return db.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		current, err := r.category(ctx, tx, id)
		if err != nil {
			return err
		}
		if !current.Custom {
			return ErrSystemCategory
		}
		if mergeInto == nil {
			return deleteUnused(ctx, tx, id)
		}
		target, err := r.category(ctx, tx, *mergeInto)
		if errors.Is(err, ErrCategoryNotFound) || (err == nil && (target.ID == id || ptrEq(target.ParentID, id))) {
			return ErrInvalidMerge
		}
		if err != nil {
			return err
		}
		targetMain := target
		if target.ParentID != nil {
			if targetMain, err = r.category(ctx, tx, *target.ParentID); err != nil {
				return err
			}
		}
		if err := mergeCategory(ctx, tx, current, target, targetMain); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM user_category WHERE id = $1`, id)
		return eris.Wrap(err, "delete category")
	})
}

func ptrEq(p *uuid.UUID, id uuid.UUID) bool { return p != nil && *p == id }

func deleteUnused(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var used bool
	err := tx.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM category_assignment WHERE user_category_id = $1) OR
		EXISTS (SELECT 1 FROM rule WHERE user_category_id = $1) OR
		EXISTS (SELECT 1 FROM budget WHERE user_category_id = $1) OR
		EXISTS (SELECT 1 FROM user_category WHERE parent_user_category_id = $1)`, id).Scan(&used)
	if err != nil {
		return eris.Wrap(err, "check category use")
	}
	if used {
		return ErrCategoryInUse
	}
	_, err = tx.Exec(ctx, `DELETE FROM user_category WHERE id = $1`, id)
	return eris.Wrap(err, "delete category")
}

func mergeCategory(ctx context.Context, tx pgx.Tx, from, into, intoMain *Category) error {
	intoSystem, intoUser := splitRef(into)
	mainSystem, mainUser := splitRef(intoMain)
	steps := []struct {
		what string
		sql  string
		args []any
	}{
		{"merge split lines", `UPDATE category_assignment t SET amount_minor = t.amount_minor + s.amount_minor
			FROM category_assignment s
			WHERE s.user_category_id = $1 AND t.category_id = $2 AND t.transaction_id = s.transaction_id`, []any{from.ID, into.ID}},
		{"drop merged split lines", `DELETE FROM category_assignment s WHERE s.user_category_id = $1 AND EXISTS (
			SELECT 1 FROM category_assignment t WHERE t.category_id = $2 AND t.transaction_id = s.transaction_id)`, []any{from.ID, into.ID}},
		{"move assignments", `UPDATE category_assignment SET system_category_id = $2, user_category_id = $3
			WHERE user_category_id = $1`, []any{from.ID, intoSystem, intoUser}},
		{"move rules", `UPDATE rule SET system_category_id = $2, user_category_id = $3 WHERE user_category_id = $1`,
			[]any{from.ID, intoSystem, intoUser}},
		{"merge budgets", `UPDATE budget t SET amount_minor = t.amount_minor + s.amount_minor FROM budget s
			WHERE s.user_category_id = $1 AND t.category_id = $2 AND t.month = s.month AND t.currency = s.currency`, []any{from.ID, into.ID}},
		// A month allows one line per category whatever its currency, so a line in another currency than the target's goes.
		{"drop merged budgets", `DELETE FROM budget s WHERE s.user_category_id = $1 AND EXISTS (
			SELECT 1 FROM budget t WHERE t.category_id = $2 AND t.month = s.month)`, []any{from.ID, into.ID}},
		{"move budgets", `UPDATE budget SET system_category_id = $2, user_category_id = $3 WHERE user_category_id = $1`,
			[]any{from.ID, intoSystem, intoUser}},
		{"move subcategories", `UPDATE user_category SET parent_category_id = $2, parent_user_category_id = $3, kind = $4
			WHERE parent_user_category_id = $1`, []any{from.ID, mainSystem, mainUser, string(intoMain.Kind)}},
	}
	for _, s := range steps {
		if _, err := tx.Exec(ctx, s.sql, s.args...); err != nil {
			return eris.Wrap(err, s.what)
		}
	}
	return nil
}

func (r *Repo) CategoryUsage(ctx context.Context) (map[uuid.UUID]CategoryUsage, error) {
	rows, err := r.pool.Query(ctx, `
		WITH assigned AS (SELECT category_id, count(*) AS n FROM category_assignment GROUP BY 1),
			ruled AS (SELECT category_id, count(*) AS n FROM rule GROUP BY 1)
		SELECT c.id, coalesce(a.n, 0), coalesce(r.n, 0)
		FROM any_category c
		LEFT JOIN assigned a ON a.category_id = c.id
		LEFT JOIN ruled r ON r.category_id = c.id`)
	if err != nil {
		return nil, eris.Wrap(err, "count category use")
	}
	defer rows.Close()
	usage := map[uuid.UUID]CategoryUsage{}
	for rows.Next() {
		var id uuid.UUID
		var u CategoryUsage
		if err := rows.Scan(&id, &u.Transactions, &u.Rules); err != nil {
			return nil, eris.Wrap(err, "read category use")
		}
		usage[id] = u
	}
	return usage, eris.Wrap(rows.Err(), "read category use")
}
