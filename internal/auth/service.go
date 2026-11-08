package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// IdleTimeout is how long a session lasts after its last use.
	IdleTimeout = 30 * 24 * time.Hour
	// MaxLifetime is the hard cap on a session's total lifetime.
	MaxLifetime = 90 * 24 * time.Hour

	minPasswordLen = 8
	// maxPasswordBytes bounds the work an attacker can force per request.
	maxPasswordBytes = 256
	maxEmailLen      = 254
)

var (
	ErrInvalidInput       = errors.New("auth: invalid input")
	ErrUsernameTaken      = errors.New("auth: username already taken")
	ErrEmailTaken         = errors.New("auth: email already in use")
	ErrInvalidCredentials = errors.New("auth: invalid username or password")
	ErrNoSession          = errors.New("auth: no valid session")

	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)
)

// Account is the authenticated player's identity.
type Account struct {
	ID       int64
	Username string
}

// Service provides account and session operations backed by PostgreSQL.
type Service struct {
	DB     *sql.DB
	params Params
	Now    func() time.Time

	dummyHash string
}

// NewService returns a Service using DefaultParams and the system clock.
func NewService(db *sql.DB) (*Service, error) {
	return newService(db, DefaultParams)
}

func newService(db *sql.DB, params Params) (*Service, error) {
	s := &Service{DB: db, params: params, Now: time.Now}
	if err := s.init(); err != nil {
		return nil, err
	}
	return s, nil
}

// init prepares a dummy hash used to equalise login timing for unknown users.
func (s *Service) init() error {
	h, err := HashPassword("dummy password for timing", s.params)
	if err != nil {
		return err
	}
	s.dummyHash = h
	return nil
}

// Register creates an account. email may be empty.
func (s *Service) Register(ctx context.Context, username, email, password string) (int64, error) {
	if !usernamePattern.MatchString(username) {
		return 0, fmt.Errorf("%w: username must be 3-32 letters, digits, underscores, or hyphens", ErrInvalidInput)
	}
	if utf8.RuneCountInString(password) < minPasswordLen || len(password) > maxPasswordBytes {
		return 0, fmt.Errorf("%w: password must be %d-%d characters", ErrInvalidInput, minPasswordLen, maxPasswordBytes)
	}
	var emailArg any
	if email != "" {
		addr, err := mail.ParseAddress(email)
		if err != nil || addr.Name != "" || addr.Address != email || len(email) > maxEmailLen {
			return 0, fmt.Errorf("%w: email address is not valid", ErrInvalidInput)
		}
		emailArg = email
	}

	hash, err := HashPassword(password, s.params)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRowContext(ctx,
		`INSERT INTO accounts (username, email, password_hash, turns_updated_at)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		username, emailArg, hash, s.Now()).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "accounts_email_lower_key" {
				return 0, ErrEmailTaken
			}
			return 0, ErrUsernameTaken
		}
		return 0, fmt.Errorf("insert account: %w", err)
	}
	return id, nil
}

// Login verifies credentials and returns a new session token. The token is
// shown only here; the database stores only its hash.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	if len(password) > maxPasswordBytes {
		return "", ErrInvalidCredentials
	}
	var id int64
	var hash string
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, password_hash FROM accounts WHERE lower(username) = lower($1)`, username).Scan(&id, &hash)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("look up account: %w", err)
	}
	if !found {
		hash = s.dummyHash
	}
	// Verify even for unknown users so response time doesn't reveal which exist.
	ok, verr := VerifyPassword(password, hash)
	if verr != nil {
		return "", fmt.Errorf("verify stored password hash: %w", verr)
	}
	if !ok || !found {
		return "", ErrInvalidCredentials
	}

	token, err := newToken()
	if err != nil {
		return "", err
	}
	now := s.Now()
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, account_id, created_at, last_seen_at, expires_at)
		 VALUES ($1, $2, $3, $3, $4)`,
		hashToken(token), id, now, now.Add(IdleTimeout))
	if err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}
	return token, nil
}

// Authenticate validates a session token and extends its idle expiry, never
// beyond MaxLifetime from creation.
func (s *Service) Authenticate(ctx context.Context, token string) (Account, error) {
	now := s.Now()
	var acct Account
	err := s.DB.QueryRowContext(ctx,
		`WITH s AS (
		     UPDATE sessions
		        SET last_seen_at = GREATEST(last_seen_at, $2::timestamptz),
		            expires_at = LEAST(GREATEST(last_seen_at, $2::timestamptz) + $3 * interval '1 second',
		                               created_at + $4 * interval '1 second')
		      WHERE token_hash = $1 AND expires_at > $2
		  RETURNING account_id)
		 SELECT a.id, a.username FROM accounts a JOIN s ON s.account_id = a.id`,
		hashToken(token), now, IdleTimeout.Seconds(), MaxLifetime.Seconds(),
	).Scan(&acct.ID, &acct.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNoSession
	}
	if err != nil {
		return Account{}, fmt.Errorf("authenticate: %w", err)
	}
	return acct, nil
}

// Logout revokes a session. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, token string) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token)); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions that can no longer be used.
func (s *Service) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, s.Now())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
