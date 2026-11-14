package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const resetTokenLifetime = 30 * time.Minute

var (
	ErrRecoveryUnavailable = errors.New("auth: password recovery unavailable")
	ErrInvalidResetToken   = errors.New("auth: invalid or expired password reset token")
	ErrTokenResetUnknown   = errors.New("auth: password reset outcome unknown; try the new password or request another reset")
)

// IssueResetToken issues a single-use token to a trusted delivery component.
// Callers must not expose whether the email is eligible for recovery.
func (s *Service) IssueResetToken(ctx context.Context, email string) (string, error) {
	if email == "" || len(email) > maxEmailLen {
		return "", ErrRecoveryUnavailable
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin reset token issuance: %w", err)
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts
		WHERE lower(email) = lower($1) AND disabled_at IS NULL FOR UPDATE`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", rollbackAuth(tx, ErrRecoveryUnavailable)
	}
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("lock account for reset token: %w", err))
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO password_reset_tokens (account_id, token_hash, expires_at)
		VALUES ($1, $2, clock_timestamp() + $3 * interval '1 second')
		ON CONFLICT (account_id) DO UPDATE
		SET token_hash = EXCLUDED.token_hash, expires_at = EXCLUDED.expires_at`,
		id, hashToken(token), int64(resetTokenLifetime.Seconds()))
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("store reset token: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit reset token issuance; request a new token: %w", err)
	}
	return token, nil
}

// RedeemResetToken consumes a valid token, replaces the password, and revokes
// existing sessions atomically.
func (s *Service) RedeemResetToken(ctx context.Context, token, password string) error {
	if len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return ErrInvalidResetToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return ErrInvalidResetToken
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	tokenHash := hashToken(token)
	var id int64
	err = s.DB.QueryRowContext(ctx, `SELECT account_id FROM password_reset_tokens
		WHERE token_hash = $1 AND expires_at > clock_timestamp()`, tokenHash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidResetToken
	}
	if err != nil {
		return fmt.Errorf("look up reset token: %w", err)
	}
	passwordHash, err := HashPassword(password, s.params)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin token password reset: %w", err)
	}
	var lockedID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts
		WHERE id = $1 AND disabled_at IS NULL FOR UPDATE`, id).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrInvalidResetToken)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("lock account for token reset: %w", err))
	}
	var consumedID int64
	err = tx.QueryRowContext(ctx, `DELETE FROM password_reset_tokens
		WHERE account_id = $1 AND token_hash = $2 AND expires_at > clock_timestamp()
		RETURNING account_id`, lockedID, tokenHash).Scan(&consumedID)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrInvalidResetToken)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("consume reset token: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET password_hash = $1 WHERE id = $2`, passwordHash, consumedID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("update token-reset password: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE account_id = $1`, consumedID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("revoke token-reset sessions: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit token password reset: %w", ErrTokenResetUnknown, err)
	}
	return nil
}
