package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/platform/db"
)

const MinPasswordLength = 10

const RefreshCookieName = "coinbook_refresh"

var (
	ErrInvalidCredentials = eris.New("identity: invalid credentials")
	ErrPasswordTooShort   = eris.New("identity: password too short")
	ErrEmailTaken         = eris.New("identity: a user with this email already exists")
	ErrEmailNotVerified   = eris.New("identity: the provider has not verified this email")
	ErrInvalidToken       = eris.New("identity: invalid session token")
)

type TokenType string

const (
	AccessToken  TokenType = "access"
	RefreshToken TokenType = "refresh"
)

type User struct {
	ID        uuid.UUID
	Email     string
	CreatedAt time.Time
}

// ExternalIdentity is what a sign-in provider vouches for.
type ExternalIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
}

type Claims struct {
	jwt.RegisteredClaims
	UserID    uuid.UUID `json:"uid"`
	TokenType TokenType `json:"typ"`
}

type Service struct {
	pool       *db.Pool
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewService(pool *db.Pool, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{pool: pool, secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL}
}

func (s *Service) Register(ctx context.Context, email, password string) (*User, error) {
	email = normalizeEmail(email)
	if !strings.Contains(email, "@") {
		return nil, ErrInvalidCredentials
	}
	if len([]rune(password)) < MinPasswordLength {
		return nil, ErrPasswordTooShort
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{Email: email}
	err = s.pool.QueryRow(ctx, `INSERT INTO app_user (email, password_hash) VALUES ($1, $2) RETURNING id, created_at`,
		email, hash).Scan(&u.ID, &u.CreatedAt)
	if db.IsUniqueViolation(err) {
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, eris.Wrap(err, "create user")
	}
	return u, nil
}

func (s *Service) Login(ctx context.Context, email, password string) (*User, error) {
	u := &User{}
	var hash *string
	err := s.pool.QueryRow(ctx, `SELECT id, email, created_at, password_hash FROM app_user WHERE email = $1`,
		normalizeEmail(email)).Scan(&u.ID, &u.Email, &u.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		verifyPassword(dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, eris.Wrap(err, "find user")
	}
	// A user created through Google has no password: fail the same way as a wrong one.
	if hash == nil {
		verifyPassword(dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	if !verifyPassword(*hash, password) {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// SignInWithGoogle returns the user already linked to the Google account, or creates
// one. It never attaches the account to an existing password user: registration does
// not verify emails, so that user could be anyone who typed the address first.
func (s *Service) SignInWithGoogle(ctx context.Context, id ExternalIdentity) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx, `SELECT id, email, created_at FROM app_user WHERE google_sub = $1`, id.Subject).
		Scan(&u.ID, &u.Email, &u.CreatedAt)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, eris.Wrap(err, "find user by google account")
	}
	email := normalizeEmail(id.Email)
	if !id.EmailVerified || !strings.Contains(email, "@") {
		return nil, ErrEmailNotVerified
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO app_user (email, google_sub) VALUES ($1, $2) RETURNING id, email, created_at`,
		email, id.Subject).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if db.IsUniqueViolation(err) {
		// Two callbacks for one new account can race: the loser finds the winner's row.
		if err := s.pool.QueryRow(ctx, `SELECT id, email, created_at FROM app_user WHERE google_sub = $1`, id.Subject).
			Scan(&u.ID, &u.Email, &u.CreatedAt); err == nil {
			return u, nil
		}
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, eris.Wrap(err, "create user from google account")
	}
	return u, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx, `SELECT id, email, created_at FROM app_user WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	return u, eris.Wrap(err, "get user")
}

func (s *Service) Mint(userID uuid.UUID, typ TokenType) (string, error) {
	ttl := s.accessTTL
	if typ == RefreshToken {
		ttl = s.refreshTTL
	}
	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        uuid.NewString(),
		},
		UserID:    userID,
		TokenType: typ,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	return signed, eris.Wrap(err, "sign token")
}

func (s *Service) Parse(raw string, want TokenType) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || claims.TokenType != want {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (s *Service) AccessTTL() time.Duration { return s.accessTTL }

func (s *Service) RefreshTTL() time.Duration { return s.refreshTTL }

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// dummyHash keeps a login for an unknown email as slow as one with a wrong password.
const dummyHash = "$argon2id$v=19$m=65536,t=3,p=2$c29tZXNhbHRzb21lc2FsdA$J1y8Dz0d4QmKkP9mV7yQx1o2c3VtYmFkaGFzaDAwMDA"
