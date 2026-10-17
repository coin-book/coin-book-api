package db

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"
	pgxuuid "github.com/vgarvardt/pgx-google-uuid/v5"
)

const statementTimeout = "60s"

// appRole is created by migration 008 without superuser rights, because superusers
// bypass row-level security. The API logs in as it; the owner only runs migrations.
const appRole = "coinbook_app"

const resetBudget = 5 * time.Second

// Pool runs every query on the connection the request's scope reserved, so row-level
// security sees the signed-in user; without a scope it runs on the shared pool, where
// the policies return nothing.
type Pool struct {
	raw *pgxpool.Pool
}

// PrepareAppRole lets the application role log in with the owner's password, so the
// deployment needs one secret. It runs as the owner, after the migrations. The role is
// cluster-wide: two coin-book databases in one cluster must share the owner password.
func PrepareAppRole(ctx context.Context, ownerConnStr string) error {
	cfg, err := pgx.ParseConfig(ownerConnStr)
	if err != nil {
		return eris.Wrap(err, "parse postgres config")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return eris.Wrap(err, "connect as owner")
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return eris.Wrap(err, "begin")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	grant := `ALTER ROLE ` + appRole + ` LOGIN`
	if cfg.Password != "" {
		// ALTER ROLE takes no bind parameters; the password travels through a transaction-local setting.
		if _, err := tx.Exec(ctx, `SELECT set_config('coinbook.app_password', $1, true)`, cfg.Password); err != nil {
			return eris.Wrap(err, "stage application password")
		}
		grant = `DO $$ BEGIN EXECUTE format('ALTER ROLE ` + appRole + ` LOGIN PASSWORD %L', current_setting('coinbook.app_password')); END $$`
	}
	if _, err := tx.Exec(ctx, grant); err != nil {
		return eris.Wrap(err, "let the application role log in")
	}
	return eris.Wrap(tx.Commit(ctx), "commit")
}

// Open connects as the application role: migrations and PrepareAppRole must already
// have run.
func Open(ctx context.Context, ownerConnStr string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(ownerConnStr)
	if err != nil {
		return nil, eris.Wrap(err, "parse postgres config")
	}
	cfg.ConnConfig.User = appRole
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = statementTimeout
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxuuid.Register(conn.TypeMap())
		return nil
	}
	cfg.AfterRelease = func(conn *pgx.Conn) bool {
		ctx, cancel := context.WithTimeout(context.Background(), resetBudget)
		defer cancel()
		_, err := conn.Exec(ctx, "RESET app.user_id")
		return err == nil
	}

	raw, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, eris.Wrap(err, "create postgres pool")
	}
	pool := &Pool{raw: raw}
	if err := pool.verifyRowSecurityApplies(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return pool, nil
}

func (p *Pool) verifyRowSecurityApplies(ctx context.Context) error {
	var bypasses bool
	// Membership in another role would let SET ROLE reach that role's privileges.
	err := p.raw.QueryRow(ctx, `
		SELECT r.rolsuper OR r.rolbypassrls OR EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member = r.oid)
		FROM pg_roles r WHERE r.rolname = session_user`).Scan(&bypasses)
	if err != nil {
		return eris.Wrap(err, "inspect database role")
	}
	if bypasses {
		return eris.Errorf("database role %s could bypass row-level security", appRole)
	}
	return nil
}

func (p *Pool) Close() { p.raw.Close() }

func (p *Pool) Ping(ctx context.Context) error { return p.raw.Ping(ctx) }

// scope is a request's claim on one connection with app.user_id set. The connection is
// taken on the first query, not when the request arrives, so a slow upload does not
// hold one of the few connections while its body streams in.
type scope struct {
	pool   *pgxpool.Pool
	userID uuid.UUID

	mu     sync.Mutex
	conn   *pgxpool.Conn
	closed bool
}

var ErrScopeReleased = eris.New("db: the request's database scope has already been released")

func (s *scope) connection(ctx context.Context) (*pgxpool.Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrScopeReleased
	}
	if s.conn != nil {
		return s.conn, nil
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, eris.Wrap(err, "acquire connection")
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('app.user_id', $1, false)`, s.userID.String()); err != nil {
		conn.Release()
		return nil, eris.Wrap(err, "scope connection to user")
	}
	s.conn = conn
	return conn, nil
}

func (s *scope) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.conn != nil {
		s.conn.Release()
		s.conn = nil
	}
}

type ctxScopeKey struct{}

var ErrScopeMismatch = eris.New("db: the context is already scoped to another user")

// ScopeToUser returns a context whose queries through the Pool run as userID. The
// caller releases the scope when the request ends. A context already scoped to the
// same user is returned unchanged; on error the caller's context comes back.
func (p *Pool) ScopeToUser(ctx context.Context, userID uuid.UUID) (context.Context, func(), error) {
	if existing, ok := ctx.Value(ctxScopeKey{}).(*scope); ok {
		if existing.userID != userID {
			return ctx, func() {}, ErrScopeMismatch
		}
		return ctx, func() {}, nil
	}
	s := &scope{pool: p.raw, userID: userID}
	return context.WithValue(ctx, ctxScopeKey{}, s), s.release, nil
}

type connection interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

func (p *Pool) on(ctx context.Context) (connection, error) {
	if s, ok := ctx.Value(ctxScopeKey{}).(*scope); ok {
		return s.connection(ctx)
	}
	return p.raw, nil
}

func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c, err := p.on(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return c.Exec(ctx, sql, args...)
}

func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c, err := p.on(ctx)
	if err != nil {
		return nil, err
	}
	return c.Query(ctx, sql, args...)
}

func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c, err := p.on(ctx)
	if err != nil {
		return errRow{err}
	}
	return c.QueryRow(ctx, sql, args...)
}

func (p *Pool) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error) {
	c, err := p.on(ctx)
	if err != nil {
		return 0, err
	}
	return c.CopyFrom(ctx, table, columns, src)
}

func (p *Pool) Begin(ctx context.Context) (pgx.Tx, error) {
	c, err := p.on(ctx)
	if err != nil {
		return nil, err
	}
	return c.Begin(ctx)
}

// errRow carries a connection error to Scan, where pgx.Row callers expect errors.
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// WaitReady blocks until the database accepts connections, before migrations run.
func WaitReady(ctx context.Context, connStr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		var conn *pgx.Conn
		if conn, err = pgx.Connect(ctx, connStr); err == nil {
			err = conn.Ping(ctx)
			_ = conn.Close(ctx)
			if err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return eris.Wrapf(err, "postgres not ready after %s", timeout)
}

// Querier is satisfied by the pool and a transaction, so a repository
// method can run inside a caller's transaction or on its own.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, src pgx.CopyFromSource) (int64, error)
}

func WithTx(ctx context.Context, pool *Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return eris.Wrap(err, "begin transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return eris.Wrap(tx.Commit(ctx), "commit transaction")
}

func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
