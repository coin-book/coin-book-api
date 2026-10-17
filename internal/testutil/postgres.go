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
	"github.com/rotisserie/eris"
	postgrestc "github.com/testcontainers/testcontainers-go/modules/postgres"

	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/migrate"
)

type Postgres struct {
	container *postgrestc.PostgresContainer
	Pool      *db.Pool
	DSN       string
	// owner is the schema owner's connection: truncating between tests is not something the API role may do.
	owner    *pgx.Conn
	truncate string
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
	if err := db.WaitReady(ctx, connStr, 30*time.Second); err != nil {
		_ = container.Terminate(ctx)
		return nil, err
	}
	if err := migrate.Up(connStr); err != nil {
		_ = container.Terminate(ctx)
		return nil, err
	}
	if err := db.PrepareAppRole(ctx, connStr); err != nil {
		_ = container.Terminate(ctx)
		return nil, err
	}
	pool, err := db.Open(ctx, connStr)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, err
	}
	owner, err := pgx.Connect(ctx, connStr)
	if err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		return nil, eris.Wrap(err, "connect as owner")
	}
	pg := &Postgres{container: container, Pool: pool, DSN: connStr, owner: owner}
	if pg.truncate, err = truncateStatement(ctx, owner); err != nil {
		pg.Close(ctx)
		return nil, err
	}
	return pg, nil
}

// TruncateAll empties every table except the seeded reference data (taxonomy, bank register).
func (pg *Postgres) TruncateAll(t *testing.T) {
	t.Helper()
	if _, err := pg.owner.Exec(context.Background(), pg.truncate); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func (pg *Postgres) Close(ctx context.Context) {
	if pg.owner != nil {
		_ = pg.owner.Close(ctx)
	}
	if pg.Pool != nil {
		pg.Pool.Close()
	}
	if pg.container != nil {
		if err := pg.container.Terminate(ctx); err != nil {
			log.Printf("terminate postgres container: %v", err)
		}
	}
}

func truncateStatement(ctx context.Context, owner *pgx.Conn) (string, error) {
	rows, err := owner.Query(ctx, `
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
