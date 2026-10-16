package agentaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rotisserie/eris"
)

const TokenPrefix = "coinbook_pat_" //nolint:gosec // G101: a prefix, not a credential

const secretLength = 40

const MaxTTL = 365 * 24 * time.Hour

type Scope string

const (
	ScopeAccountsRead     Scope = "accounts:read"
	ScopeTransactionsRead Scope = "transactions:read"
	ScopeCategoriesRead   Scope = "categories:read"
	ScopeBudgetsRead      Scope = "budgets:read"
	ScopeReportsRead      Scope = "reports:read"
)

var AllScopes = []Scope{ScopeAccountsRead, ScopeTransactionsRead, ScopeCategoriesRead, ScopeBudgetsRead, ScopeReportsRead}

var (
	ErrInvalidToken = eris.New("agentaccess: invalid token")
	ErrUnknownScope = eris.New("agentaccess: unknown scope")
	ErrNoScopes     = eris.New("agentaccess: at least one scope is required")
	ErrTTLTooLong   = eris.New("agentaccess: expiry too far out")
	ErrNotFound     = eris.New("agentaccess: token not found")
)

type Token struct {
	ID         uuid.UUID
	Name       string
	Last4      string
	Scopes     []Scope
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

func (t *Token) Allows(scope Scope) bool { return slices.Contains(t.Scopes, scope) }

type AuditEntry struct {
	ID          int64
	TokenID     uuid.UUID
	TokenName   string
	Operation   string
	Status      int
	ResultCount *int
	OccurredAt  time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func Looks(raw string) bool { return strings.HasPrefix(raw, TokenPrefix) }

func (r *Repo) Create(ctx context.Context, name string, scopes []Scope, ttl *time.Duration) (*Token, string, error) {
	if len(scopes) == 0 {
		return nil, "", ErrNoScopes
	}
	for _, s := range scopes {
		if !slices.Contains(AllScopes, s) {
			return nil, "", eris.Wrapf(ErrUnknownScope, "%s", s)
		}
	}
	var expiresAt *time.Time
	if ttl != nil {
		if *ttl > MaxTTL || *ttl <= 0 {
			return nil, "", ErrTTLTooLong
		}
		at := time.Now().Add(*ttl)
		expiresAt = &at
	}
	raw, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	t := &Token{Name: strings.TrimSpace(name), Last4: raw[len(raw)-4:], Scopes: scopes, ExpiresAt: expiresAt}
	err = r.pool.QueryRow(ctx, `
		INSERT INTO agent_token (name, token_hash, last4, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		t.Name, hashOf(raw), t.Last4, scopeStrings(scopes), expiresAt).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return nil, "", eris.Wrap(err, "insert agent token")
	}
	return t, raw, nil
}

func (r *Repo) Authenticate(ctx context.Context, raw string) (*Token, error) {
	t, err := scanToken(r.pool.QueryRow(ctx, `
		UPDATE agent_token SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		RETURNING `+tokenColumns, hashOf(raw)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	return t, err
}

func (r *Repo) List(ctx context.Context) ([]*Token, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+tokenColumns+` FROM agent_token ORDER BY created_at DESC`)
	if err != nil {
		return nil, eris.Wrap(err, "list agent tokens")
	}
	defer rows.Close()
	var tokens []*Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, eris.Wrap(rows.Err(), "read agent tokens")
}

func (r *Repo) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `UPDATE agent_token SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return eris.Wrap(err, "revoke agent token")
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) RecordAccess(ctx context.Context, tokenID uuid.UUID, operation string, status int, resultCount *int) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO audit_log (token_id, operation, status, result_count) VALUES ($1, $2, $3, $4)`,
		tokenID, operation, status, resultCount)
	return eris.Wrap(err, "insert audit log")
}

func (r *Repo) AuditLog(ctx context.Context, limit int, before *int64) ([]AuditEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT l.id, l.token_id, t.name, l.operation, l.status, l.result_count, l.occurred_at
		FROM audit_log l JOIN agent_token t ON t.id = l.token_id
		WHERE $2::bigint IS NULL OR l.id < $2
		ORDER BY l.id DESC LIMIT $1`, limit, before)
	if err != nil {
		return nil, eris.Wrap(err, "list audit log")
	}
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuditEntry, error) {
		var e AuditEntry
		err := row.Scan(&e.ID, &e.TokenID, &e.TokenName, &e.Operation, &e.Status, &e.ResultCount, &e.OccurredAt)
		return e, err
	})
	return entries, eris.Wrap(err, "read audit log")
}

const tokenColumns = `id, name, last4, scopes, expires_at, revoked_at, last_used_at, created_at` //nolint:gosec // G101: column list

func scanToken(row pgx.Row) (*Token, error) {
	t := &Token{}
	var scopes []string
	if err := row.Scan(&t.ID, &t.Name, &t.Last4, &scopes, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedAt); err != nil {
		return nil, err
	}
	for _, s := range scopes {
		t.Scopes = append(t.Scopes, Scope(s))
	}
	return t, nil
}

func scopeStrings(scopes []Scope) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = string(s)
	}
	return out
}

func hashOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func newSecret() (string, error) {
	var b strings.Builder
	b.WriteString(TokenPrefix)
	limit := big.NewInt(int64(len(base62)))
	for range secretLength {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", eris.Wrap(err, "generate token")
		}
		b.WriteByte(base62[n.Int64()])
	}
	return b.String(), nil
}
