// Package settings stores the user's preferences as typed values in app_setting.
package settings

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

const AutoCategorizeThreshold = "auto_categorize_threshold"

var ErrInvalidThreshold = eris.New("settings: the threshold must be 0 (off) or between 0.5 and 1")

const minThreshold = 0.5

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Threshold is the classifier confidence above which imports categorise on their own;
// 0 means the classifier only suggests.
func (r *Repo) Threshold(ctx context.Context, q db.Querier) (float64, error) {
	if q == nil {
		q = r.pool
	}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = $1`, AutoCategorizeThreshold).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, eris.Wrap(err, "read setting")
	}
	var threshold float64
	return threshold, eris.Wrap(json.Unmarshal(raw, &threshold), "decode setting")
}

func (r *Repo) SetThreshold(ctx context.Context, threshold float64) error {
	if threshold != 0 && (threshold < minThreshold || threshold > 1) {
		return ErrInvalidThreshold
	}
	raw, err := json.Marshal(threshold)
	if err != nil {
		return eris.Wrap(err, "encode setting")
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO app_setting (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, AutoCategorizeThreshold, raw)
	return eris.Wrap(err, "write setting")
}
