package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	pendingSignupLifetime   = 30 * time.Minute
	pendingSignupPurgeBatch = 64
	maxPendingSignupCleanup = 1000
)

var (
	// ErrPendingSignupUnavailable means the address already belongs to an
	// account or no mail should be sent; callers still return a generic success.
	ErrPendingSignupUnavailable = errors.New("auth: pending signup unavailable")
	ErrInvalidPendingSignup     = errors.New("auth: invalid or expired signup code")
)

// StartPendingSignup stores a deferred registration and returns a one-time code.
// When the email already belongs to an account it returns ErrPendingSignupUnavailable
// so handlers can respond identically without creating a usable account.
func (s *Service) StartPendingSignup(ctx context.Context, username, email, password string) (string, string, error) {
	if !usernamePattern.MatchString(username) {
		return "", "", fmt.Errorf("%w: username must be 3-32 letters, digits, underscores, or hyphens", ErrInvalidInput)
	}
	if err := validatePassword(password); err != nil {
		return "", "", err
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email || email == "" || len(email) > maxEmailLen {
		return "", "", fmt.Errorf("%w: email address is not valid", ErrInvalidInput)
	}
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return "", "", err
	}
	token, err := newToken()
	if err != nil {
		return "", "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("begin pending signup: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_signups
		WHERE id IN (
			SELECT id FROM pending_signups
			WHERE expires_at <= clock_timestamp()
			ORDER BY expires_at LIMIT $1
		)`, pendingSignupPurgeBatch); err != nil {
		return "", "", rollbackAuth(tx, fmt.Errorf("purge expired pending signups: %w", err))
	}
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM accounts WHERE lower(username) = lower($1))`, username).Scan(&exists)
	if err != nil {
		return "", "", rollbackAuth(tx, fmt.Errorf("check username for pending signup: %w", err))
	}
	if exists {
		return "", "", rollbackAuth(tx, ErrUsernameTaken)
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM accounts WHERE lower(email) = lower($1))`, email).Scan(&exists)
	if err != nil {
		return "", "", rollbackAuth(tx, fmt.Errorf("check email for pending signup: %w", err))
	}
	if exists {
		return "", "", rollbackAuth(tx, ErrPendingSignupUnavailable)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pending_signups
		(username, email, password_hash, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, clock_timestamp() + $5 * interval '1 second')
		ON CONFLICT ((lower(email))) DO UPDATE SET
			username = EXCLUDED.username,
			password_hash = EXCLUDED.password_hash,
			token_hash = EXCLUDED.token_hash,
			expires_at = EXCLUDED.expires_at,
			created_at = now()`,
		username, email, hash, hashToken(token), int64(pendingSignupLifetime.Seconds()))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", "", rollbackAuth(tx, ErrUsernameTaken)
		}
		return "", "", rollbackAuth(tx, fmt.Errorf("store pending signup: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit pending signup: %w", err)
	}
	return token, email, nil
}

// CompletePendingSignup creates a verified account from a pending signup code.
func (s *Service) CompletePendingSignup(ctx context.Context, token string) (int64, error) {
	if len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return 0, ErrInvalidPendingSignup
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return 0, ErrInvalidPendingSignup
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin complete pending signup: %w", err)
	}
	var username, email, passwordHash string
	err = tx.QueryRowContext(ctx, `DELETE FROM pending_signups
		WHERE token_hash = $1 AND expires_at > clock_timestamp()
		RETURNING username, email, password_hash`, hashToken(token)).
		Scan(&username, &email, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, rollbackAuth(tx, ErrInvalidPendingSignup)
	}
	if err != nil {
		return 0, rollbackAuth(tx, fmt.Errorf("consume pending signup: %w", err))
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO accounts
		(username, email, password_hash, turns_balance, turns_updated_at, email_verified_at, verified_email)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp(), $2) RETURNING id`,
		username, email, passwordHash, s.initialTurns, s.Now()).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "accounts_email_lower_key" {
				return 0, rollbackAuth(tx, ErrPendingSignupUnavailable)
			}
			return 0, rollbackAuth(tx, ErrUsernameTaken)
		}
		return 0, rollbackAuth(tx, fmt.Errorf("create account from pending signup: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit completed signup; check whether the account exists before retrying: %w", err)
	}
	return id, nil
}

// DeleteExpiredPendingSignups removes expired deferred signups in a bounded batch.
// Call periodically from maintenance; batchSize must be between 1 and 1000.
func (s *Service) DeleteExpiredPendingSignups(ctx context.Context, batchSize int) (int64, error) {
	if batchSize < 1 || batchSize > maxPendingSignupCleanup {
		return 0, fmt.Errorf("auth: pending signup cleanup batch must be between 1 and %d", maxPendingSignupCleanup)
	}
	result, err := s.DB.ExecContext(ctx, `DELETE FROM pending_signups
		WHERE id IN (
			SELECT id FROM pending_signups
			WHERE expires_at <= clock_timestamp()
			ORDER BY expires_at LIMIT $1
		)`, batchSize)
	if err != nil {
		return 0, fmt.Errorf("delete expired pending signups: %w", err)
	}
	return result.RowsAffected()
}
