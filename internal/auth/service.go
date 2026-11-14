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
	ErrInvalidInput         = errors.New("auth: invalid input")
	ErrUsernameTaken        = errors.New("auth: username already taken")
	ErrEmailTaken           = errors.New("auth: email already in use")
	ErrInvalidCredentials   = errors.New("auth: invalid username or password")
	ErrNoSession            = errors.New("auth: no valid session")
	ErrAccountNotFound      = errors.New("auth: account not found")
	ErrResetOutcomeUnknown  = errors.New("auth: password reset outcome unknown; retry with the same password")
	ErrAccountStatusUnknown = errors.New("auth: account status outcome unknown; inspect the account before retrying")

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

	initialTurns int64
	dummyHash    string
}

// NewService returns a Service using DefaultParams and the system clock.
func NewService(db *sql.DB) (*Service, error) {
	return newServiceWithInitialTurns(db, DefaultParams, 0)
}

// NewServiceWithInitialTurns returns a Service whose new accounts start with
// the supplied nonnegative Turns balance.
func NewServiceWithInitialTurns(db *sql.DB, initialTurns int64) (*Service, error) {
	if initialTurns < 0 {
		return nil, errors.New("auth: initial Turns must be nonnegative")
	}
	return newServiceWithInitialTurns(db, DefaultParams, initialTurns)
}

func newService(db *sql.DB, params Params) (*Service, error) {
	return newServiceWithInitialTurns(db, params, 0)
}

func newServiceWithInitialTurns(db *sql.DB, params Params, initialTurns int64) (*Service, error) {
	s := &Service{DB: db, params: params, Now: time.Now, initialTurns: initialTurns}
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
	if err := validatePassword(password); err != nil {
		return 0, err
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
		`INSERT INTO accounts (username, email, password_hash, turns_balance, turns_updated_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		username, emailArg, hash, s.initialTurns, s.Now()).Scan(&id)
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
		`SELECT id, password_hash FROM accounts WHERE lower(username) = lower($1) AND disabled_at IS NULL`, username).Scan(&id, &hash)
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

	return s.createSession(ctx, id, hash)
}

func (s *Service) createSession(ctx context.Context, id int64, verifiedHash string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin session transaction: %w", err)
	}
	var currentHash string
	// Serialize session creation with account changes, then recheck the verified hash.
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM accounts WHERE id = $1 AND disabled_at IS NULL FOR SHARE`, id).Scan(&currentHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && currentHash != verifiedHash) {
		return "", rollbackAuth(tx, ErrInvalidCredentials)
	}
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("lock account for login: %w", err))
	}
	now := s.Now()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, account_id, created_at, last_seen_at, expires_at)
		 VALUES ($1, $2, $3, $3, $4)`,
		hashToken(token), id, now, now.Add(IdleTimeout))
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("insert session: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit session: %w", err)
	}
	return token, nil
}

// ResetPassword replaces an account password and revokes its sessions atomically.
func (s *Service) ResetPassword(ctx context.Context, username, password string) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("%w: invalid username", ErrInvalidInput)
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin password reset: %w", err)
	}
	var id int64
	err = tx.QueryRowContext(ctx, `UPDATE accounts SET password_hash = $1
		WHERE lower(username) = lower($2) RETURNING id`, hash, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrAccountNotFound)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("update password: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id = $1`, id); err != nil {
		return rollbackAuth(tx, fmt.Errorf("revoke sessions: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit password reset: %w", ErrResetOutcomeUnknown, err)
	}
	return nil
}

// SetAccountEnabled changes an account's status, revoking sessions when disabled.
func (s *Service) SetAccountEnabled(ctx context.Context, username string, enabled bool) error {
	if !usernamePattern.MatchString(username) {
		return fmt.Errorf("%w: invalid username", ErrInvalidInput)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account status change: %w", err)
	}
	var id int64
	var wasDisabled bool
	err = tx.QueryRowContext(ctx, `SELECT id, disabled_at IS NOT NULL FROM accounts
		WHERE lower(username) = lower($1) FOR UPDATE`, username).Scan(&id, &wasDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrAccountNotFound)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("lock account for status change: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts
		SET disabled_at = CASE WHEN $1 THEN NULL ELSE COALESCE(disabled_at, clock_timestamp()) END
		WHERE id = $2`, enabled, id); err != nil {
		return rollbackAuth(tx, fmt.Errorf("update account status: %w", err))
	}
	if !enabled || wasDisabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id = $1`, id); err != nil {
			return rollbackAuth(tx, fmt.Errorf("revoke sessions: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit account status change: %w", ErrAccountStatusUnknown, err)
	}
	return nil
}

func validatePassword(password string) error {
	if utf8.RuneCountInString(password) < minPasswordLen || len(password) > maxPasswordBytes {
		return fmt.Errorf("%w: password must be %d-%d characters", ErrInvalidInput, minPasswordLen, maxPasswordBytes)
	}
	return nil
}

func rollbackAuth(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return errors.Join(cause, fmt.Errorf("rollback auth transaction: %w", err))
	}
	return cause
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
		 SELECT a.id, a.username FROM accounts a JOIN s ON s.account_id = a.id
		  WHERE a.disabled_at IS NULL`,
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
