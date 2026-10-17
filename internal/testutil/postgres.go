package testutil

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"
	postgrestc "github.com/testcontainers/testcontainers-go/modules/postgres"

	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/migrate"
)

type Postgres struct {
	container *postgrestc.PostgresContainer
	Pool      *pgxpool.Pool
	DSN       string
	truncate  string
}

// RunWithPostgres starts one container for a package's tests and exits with their code.
func RunWithPostgres(m *testing.M, pg **Postgres) {
	ctx := context.Background()
	p, err := NewPostgres(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start test postgres: %v\n", err)
		os.Exit(1)
	}
	*pg = p
	code := m.Run()
	p.Close(ctx)
	os.Exit(code)
}

func NewPostgres(ctx context.Context) (*Postgres, error) {
	container, err := postgrestc.Run(ctx, "postgres:18-alpine", postgrestc.BasicWaitStrategies())
	if err != nil {
		return nil, eris.Wrap(err, "start postgres container")
	}
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, eris.Wrap(err, "connection string")
	}
	pool, err := db.Open(ctx, connStr)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, err
	}
	if err := db.WaitReady(ctx, pool, 30*time.Second); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		return nil, err
	}
	if err := migrate.Up(connStr); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		return nil, err
	}
	pg := &Postgres{container: container, Pool: pool, DSN: connStr}
	if pg.truncate, err = truncateStatement(ctx, pool); err != nil {
		pg.Close(ctx)
		return nil, err
	}
	return pg, nil
}

// TruncateAll empties every table except the seeded reference data (taxonomy, bank register).
func (pg *Postgres) TruncateAll(t *testing.T) {
	t.Helper()
	if _, err := pg.Pool.Exec(context.Background(), pg.truncate); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func (pg *Postgres) Close(ctx context.Context) {
	if pg.Pool != nil {
		pg.Pool.Close()
	}
	if pg.container != nil {
		if err := pg.container.Terminate(ctx); err != nil {
			log.Printf("terminate postgres container: %v", err)
		}
	}
}

func truncateStatement(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	rows, err := pool.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename NOT IN ('category', 'budget_group', 'bank', 'schema_migrations')
		ORDER BY 1`)
	if err != nil {
		return "", eris.Wrap(err, "list tables")
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", eris.Wrap(err, "read tables")
	}
	return "TRUNCATE TABLE " + strings.Join(tables, ", ") + " CASCADE", nil
}
