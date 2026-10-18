package importing

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/importing/parser/generic"
	"coin-book-api/internal/platform/db"
)

var (
	ErrTemplateNotFound    = eris.New("importing: template not found")
	ErrInvalidTemplateName = eris.New("importing: a template needs a name of at most 80 characters")
)

const maxTemplateName = 80

// Template is a saved column layout for one bank's CSV export.
type Template struct {
	ID          uuid.UUID
	Name        string
	Layout      generic.Layout
	CreatedAt   time.Time
	UpdatedAt   time.Time
	fingerprint string
}

// TemplateRef names the template a mapped file was read by.
type TemplateRef struct {
	ID   uuid.UUID
	Name string
}

func (s *Service) Templates(ctx context.Context) ([]Template, error) {
	return listTemplates(ctx, s.pool)
}

func (s *Service) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM import_template WHERE id = $1`, id)
	if err != nil {
		return eris.Wrap(err, "delete import template")
	}
	if tag.RowsAffected() == 0 {
		return ErrTemplateNotFound
	}
	return nil
}

// Inspect splits an unknown CSV for the mapper and guesses its layout; a saved
// template whose fingerprint fits wins over the guess.
func (s *Service) Inspect(ctx context.Context, content []byte, encoding generic.Encoding, delimiter string) (*generic.Inspection, *TemplateRef, error) {
	inspection, err := generic.Inspect(content, encoding, delimiter)
	if err != nil {
		return nil, nil, err
	}
	template, err := templateFor(ctx, s.pool, content)
	if err != nil || template == nil {
		return inspection, nil, err
	}
	inspection.Suggested = template.Layout
	return inspection, &TemplateRef{ID: template.ID, Name: template.Name}, nil
}

func listTemplates(ctx context.Context, q db.Querier) ([]Template, error) {
	rows, err := q.Query(ctx, `SELECT id, name, layout, created_at, updated_at, fingerprint FROM import_template ORDER BY name`)
	if err != nil {
		return nil, eris.Wrap(err, "list import templates")
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Template, error) {
		var t Template
		var layout []byte
		if err := row.Scan(&t.ID, &t.Name, &layout, &t.CreatedAt, &t.UpdatedAt, &t.fingerprint); err != nil {
			return t, eris.Wrap(err, "read import template")
		}
		return t, eris.Wrap(json.Unmarshal(layout, &t.Layout), "decode import template")
	})
}

// templateFor finds the user's template whose fingerprint the file's column names give;
// each template knows on which row and in which encoding to look for them.
func templateFor(ctx context.Context, q db.Querier, content []byte) (*Template, error) {
	templates, err := listTemplates(ctx, q)
	if err != nil {
		return nil, err
	}
	for i, t := range templates {
		if fingerprint := t.Layout.Fingerprint(content); fingerprint != "" && fingerprint == t.fingerprint {
			return &templates[i], nil
		}
	}
	return nil, nil
}

// saveTemplate keeps one template per export shape: saving the same shape again
// renames it and replaces its layout.
func saveTemplate(ctx context.Context, q db.Querier, name string, layout generic.Layout, content []byte) (*TemplateRef, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > maxTemplateName {
		return nil, ErrInvalidTemplateName
	}
	encoded, err := json.Marshal(layout)
	if err != nil {
		return nil, eris.Wrap(err, "encode import template")
	}
	ref := &TemplateRef{Name: name}
	err = q.QueryRow(ctx, `
		INSERT INTO import_template (name, fingerprint, layout) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, fingerprint) DO UPDATE SET name = excluded.name, layout = excluded.layout, updated_at = now()
		RETURNING id`, name, layout.Fingerprint(content), encoded).Scan(&ref.ID)
	return ref, eris.Wrap(err, "save import template")
}
