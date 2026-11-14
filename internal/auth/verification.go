package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

const verificationTokenLifetime = 30 * time.Minute

var (
	ErrVerificationUnavailable  = errors.New("auth: email verification unavailable")
	ErrInvalidVerificationToken = errors.New("auth: invalid or expired verification token")
)

// EmailVerificationStatus returns the account's stored address and verification state.
func (s *Service) EmailVerificationStatus(ctx context.Context, accountID int64) (string, bool, error) {
	var email sql.NullString
	var verified bool
	err := s.DB.QueryRowContext(ctx, `SELECT email, COALESCE(email_verified_at IS NOT NULL AND email = verified_email, false) FROM accounts
		WHERE id = $1 AND disabled_at IS NULL`, accountID).Scan(&email, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrVerificationUnavailable
	}
	if err != nil {
		return "", false, fmt.Errorf("read email verification status: %w", err)
	}
	return email.String, verified, nil
}

// IssueEmailVerificationToken creates a code for the account's current address.
func (s *Service) IssueEmailVerificationToken(ctx context.Context, accountID int64) (string, string, error) {
	token, err := newToken()
	if err != nil {
		return "", "", err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("begin email verification: %w", err)
	}
	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM accounts
		WHERE id = $1 AND disabled_at IS NULL AND email IS NOT NULL
			AND (email_verified_at IS NULL OR email IS DISTINCT FROM verified_email)
		FOR UPDATE`, accountID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", rollbackAuth(tx, ErrVerificationUnavailable)
	}
	if err != nil {
		return "", "", rollbackAuth(tx, fmt.Errorf("lock account for email verification: %w", err))
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO email_verification_tokens (account_id, token_hash, email, expires_at)
		VALUES ($1, $2, $3, clock_timestamp() + $4 * interval '1 second')
		ON CONFLICT (account_id) DO UPDATE SET token_hash = EXCLUDED.token_hash,
			email = EXCLUDED.email, expires_at = EXCLUDED.expires_at`,
		accountID, hashToken(token), email, int64(verificationTokenLifetime.Seconds()))
	if err != nil {
		return "", "", rollbackAuth(tx, fmt.Errorf("store email verification token: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit email verification token; request a new code: %w", err)
	}
	return token, email, nil
}

// VerifyEmail consumes a code only for the signed-in account and unchanged address.
func (s *Service) VerifyEmail(ctx context.Context, accountID int64, token string) error {
	if len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return ErrInvalidVerificationToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return ErrInvalidVerificationToken
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin email verification: %w", err)
	}
	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM accounts
		WHERE id = $1 AND disabled_at IS NULL AND email IS NOT NULL
			AND (email_verified_at IS NULL OR email IS DISTINCT FROM verified_email)
		FOR UPDATE`, accountID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrInvalidVerificationToken)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("lock account for email verification: %w", err))
	}
	var consumedID int64
	err = tx.QueryRowContext(ctx, `DELETE FROM email_verification_tokens
		WHERE account_id = $1 AND token_hash = $2 AND email = $3 AND expires_at > clock_timestamp()
		RETURNING account_id`, accountID, hashToken(token), email).Scan(&consumedID)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrInvalidVerificationToken)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("consume email verification token: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM password_reset_tokens WHERE account_id = $1`, consumedID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("revoke old email recovery tokens: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET email_verified_at = clock_timestamp(), verified_email = email WHERE id = $1`, consumedID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("verify email: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit email verification; check status before retrying: %w", err)
	}
	return nil
}
