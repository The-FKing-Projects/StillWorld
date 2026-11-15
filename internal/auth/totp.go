package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TOTPEnrolment is returned when beginning TOTP setup. Secret and URL are shown
// once; ConfirmTOTPEnrolment must succeed before the factor is enabled.
type TOTPEnrolment struct {
	Secret string
	URL    string
}

// SetTOTPKeys configures AEAD keys used to encrypt TOTP secrets. A nil ring
// disables enrolment and blocks login for accounts that already have TOTP.
func (s *Service) SetTOTPKeys(ring *TOTPKeyRing) {
	s.totp = ring
}

// BeginTOTPEnrolment stores a pending encrypted secret for the account.
func (s *Service) BeginTOTPEnrolment(ctx context.Context, accountID int64, username string) (TOTPEnrolment, error) {
	if s.totp == nil {
		return TOTPEnrolment{}, ErrTOTPUnavailable
	}
	if accountID <= 0 || username == "" {
		return TOTPEnrolment{}, ErrInvalidInput
	}
	raw, secret, err := generateTOTPSecret()
	if err != nil {
		return TOTPEnrolment{}, err
	}
	keyID, nonce, ciphertext, err := s.totp.encrypt(raw)
	if err != nil {
		return TOTPEnrolment{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return TOTPEnrolment{}, fmt.Errorf("begin totp enrolment: %w", err)
	}
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_totp WHERE account_id = $1)`, accountID).Scan(&enabled)
	if err != nil {
		return TOTPEnrolment{}, rollbackAuth(tx, fmt.Errorf("check totp enrolment: %w", err))
	}
	if enabled {
		return TOTPEnrolment{}, rollbackAuth(tx, ErrTOTPAlreadyEnabled)
	}
	var exists bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM accounts WHERE id = $1 AND disabled_at IS NULL)`, accountID).Scan(&exists)
	if err != nil {
		return TOTPEnrolment{}, rollbackAuth(tx, fmt.Errorf("check account for totp: %w", err))
	}
	if !exists {
		return TOTPEnrolment{}, rollbackAuth(tx, ErrAccountNotFound)
	}
	expires := s.Now().Add(pendingEnrolTTL)
	_, err = tx.ExecContext(ctx, `INSERT INTO totp_pending
		(account_id, key_id, nonce, ciphertext, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (account_id) DO UPDATE SET
			key_id = EXCLUDED.key_id,
			nonce = EXCLUDED.nonce,
			ciphertext = EXCLUDED.ciphertext,
			expires_at = EXCLUDED.expires_at`,
		accountID, keyID, nonce, ciphertext, expires)
	if err != nil {
		return TOTPEnrolment{}, rollbackAuth(tx, fmt.Errorf("store pending totp: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return TOTPEnrolment{}, fmt.Errorf("commit totp enrolment: %w", err)
	}
	return TOTPEnrolment{Secret: secret, URL: otpauthURL(username, secret)}, nil
}

// ConfirmTOTPEnrolment enables TOTP after the user proves they can generate a code.
// It returns one-time recovery codes shown only here.
func (s *Service) ConfirmTOTPEnrolment(ctx context.Context, accountID int64, code string) ([]string, error) {
	if s.totp == nil {
		return nil, ErrTOTPUnavailable
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin totp confirm: %w", err)
	}
	var keyID string
	var nonce, ciphertext []byte
	var expires sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT key_id, nonce, ciphertext, expires_at
		FROM totp_pending WHERE account_id = $1 FOR UPDATE`, accountID).
		Scan(&keyID, &nonce, &ciphertext, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, rollbackAuth(tx, ErrTOTPInvalid)
	}
	if err != nil {
		return nil, rollbackAuth(tx, fmt.Errorf("load pending totp: %w", err))
	}
	now := s.Now()
	if !expires.Valid || !expires.Time.After(now) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM totp_pending WHERE account_id = $1`, accountID)
		return nil, rollbackAuth(tx, ErrTOTPInvalid)
	}
	secret, err := s.totp.decrypt(keyID, nonce, ciphertext)
	if err != nil {
		return nil, rollbackAuth(tx, err)
	}
	step, ok := verifyTOTPCode(secret, code, now, -1)
	if !ok {
		return nil, rollbackAuth(tx, ErrTOTPInvalid)
	}
	plainCodes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, rollbackAuth(tx, err)
	}
	// Re-encrypt under the active key so pending key IDs do not linger.
	activeID, activeNonce, activeCipher, err := s.totp.encrypt(secret)
	if err != nil {
		return nil, rollbackAuth(tx, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_totp
		(account_id, key_id, nonce, ciphertext, last_step, confirmed_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		accountID, activeID, activeNonce, activeCipher, step, now)
	if err != nil {
		return nil, rollbackAuth(tx, fmt.Errorf("store totp: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE account_id = $1`, accountID); err != nil {
		return nil, rollbackAuth(tx, fmt.Errorf("clear recovery codes: %w", err))
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO totp_recovery_codes (account_id, code_hash)
			VALUES ($1, $2)`, accountID, h); err != nil {
			return nil, rollbackAuth(tx, fmt.Errorf("store recovery code: %w", err))
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_pending WHERE account_id = $1`, accountID); err != nil {
		return nil, rollbackAuth(tx, fmt.Errorf("clear pending totp: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit totp confirm: %w", err)
	}
	return plainCodes, nil
}

// DisableTOTP removes TOTP after verifying the account password.
func (s *Service) DisableTOTP(ctx context.Context, accountID int64, password string) error {
	if len(password) > maxPasswordBytes {
		return ErrInvalidCredentials
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin totp disable: %w", err)
	}
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM accounts
		WHERE id = $1 AND disabled_at IS NULL FOR UPDATE`, accountID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrAccountNotFound)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("lock account for totp disable: %w", err))
	}
	ok, verr := VerifyPassword(password, hash)
	if verr != nil {
		return rollbackAuth(tx, fmt.Errorf("verify password for totp disable: %w", verr))
	}
	if !ok {
		return rollbackAuth(tx, ErrInvalidCredentials)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM account_totp WHERE account_id = $1`, accountID)
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("delete totp: %w", err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("count totp delete: %w", err))
	}
	if n == 0 {
		return rollbackAuth(tx, ErrTOTPNotEnabled)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE account_id = $1`, accountID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("delete recovery codes: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_pending WHERE account_id = $1`, accountID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("delete pending totp: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE account_id = $1`, accountID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("delete login challenges: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit totp disable: %w", err)
	}
	return nil
}

// CompleteLoginTOTP finishes a password-accepted login with a TOTP code.
func (s *Service) CompleteLoginTOTP(ctx context.Context, challengeToken, code string) (string, error) {
	return s.completeChallenge(ctx, challengeToken, func(tx *sql.Tx, accountID int64) error {
		return s.verifyAndConsumeTOTP(ctx, tx, accountID, code)
	})
}

// CompleteLoginRecoveryCode finishes a password-accepted login with a recovery code.
func (s *Service) CompleteLoginRecoveryCode(ctx context.Context, challengeToken, code string) (string, error) {
	return s.completeChallenge(ctx, challengeToken, func(tx *sql.Tx, accountID int64) error {
		return s.consumeRecoveryCode(ctx, tx, accountID, code)
	})
}

func (s *Service) completeChallenge(ctx context.Context, challengeToken string, verify func(*sql.Tx, int64) error) (string, error) {
	if challengeToken == "" {
		return "", ErrTOTPChallenge
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin totp login: %w", err)
	}
	challengeHash := hashToken(challengeToken)
	now := s.Now()
	// Resolve the account without locking the challenge first so lock order
	// matches createLoginChallengeLocked / DisableTOTP / password reset:
	// accounts, then login_challenges.
	var accountID int64
	err = tx.QueryRowContext(ctx, `SELECT account_id FROM login_challenges
		WHERE token_hash = $1 AND expires_at > $2`,
		challengeHash, now).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", rollbackAuth(tx, ErrTOTPChallenge)
	}
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("load login challenge: %w", err))
	}
	var disabled bool
	err = tx.QueryRowContext(ctx, `SELECT disabled_at IS NOT NULL FROM accounts
		WHERE id = $1 FOR UPDATE`, accountID).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) || disabled {
		return "", rollbackAuth(tx, ErrTOTPChallenge)
	}
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("lock account for totp login: %w", err))
	}
	// Refresh after the account lock so expiry is not evaluated with a stale
	// clock from before waiting on a long-held account row.
	now = s.Now()
	var lockedAccountID int64
	err = tx.QueryRowContext(ctx, `SELECT account_id FROM login_challenges
		WHERE token_hash = $1 AND expires_at > $2 FOR UPDATE`,
		challengeHash, now).Scan(&lockedAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", rollbackAuth(tx, ErrTOTPChallenge)
	}
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("lock login challenge: %w", err))
	}
	if lockedAccountID != accountID {
		return "", rollbackAuth(tx, ErrTOTPChallenge)
	}
	if err := verify(tx, accountID); err != nil {
		return "", rollbackAuth(tx, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE token_hash = $1`, challengeHash); err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("consume login challenge: %w", err))
	}
	// Password rehash already ran on the challenge-creation path (plaintext is
	// not available here). Session insert shares createSession's helper.
	token, err := s.insertSessionTx(ctx, tx, accountID)
	if err != nil {
		return "", rollbackAuth(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit totp login: %w", err)
	}
	return token, nil
}

func (s *Service) verifyAndConsumeTOTP(ctx context.Context, tx *sql.Tx, accountID int64, code string) error {
	if s.totp == nil {
		return ErrTOTPUnavailable
	}
	var keyID string
	var nonce, ciphertext []byte
	var lastStep int64
	err := tx.QueryRowContext(ctx, `SELECT key_id, nonce, ciphertext, last_step
		FROM account_totp WHERE account_id = $1 FOR UPDATE`, accountID).
		Scan(&keyID, &nonce, &ciphertext, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTOTPNotEnabled
	}
	if err != nil {
		return fmt.Errorf("load totp: %w", err)
	}
	secret, err := s.totp.decrypt(keyID, nonce, ciphertext)
	if err != nil {
		return err
	}
	step, ok := verifyTOTPCode(secret, code, s.Now(), lastStep)
	if !ok {
		return ErrTOTPInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE account_totp SET last_step = $1 WHERE account_id = $2`,
		step, accountID); err != nil {
		return fmt.Errorf("store totp step: %w", err)
	}
	// Re-encrypt under the active key when decrypt used a previous key ID.
	if keyID != s.totp.activeID {
		newID, newNonce, newCipher, err := s.totp.encrypt(secret)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE account_totp
			SET key_id = $1, nonce = $2, ciphertext = $3 WHERE account_id = $4`,
			newID, newNonce, newCipher, accountID); err != nil {
			return fmt.Errorf("rotate totp key: %w", err)
		}
	}
	return nil
}

func (s *Service) consumeRecoveryCode(ctx context.Context, tx *sql.Tx, accountID int64, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return ErrTOTPInvalid
	}
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_totp WHERE account_id = $1)`, accountID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check totp for recovery: %w", err)
	}
	if !exists {
		return ErrTOTPNotEnabled
	}
	res, err := tx.ExecContext(ctx, `UPDATE totp_recovery_codes
		SET used_at = $1
		WHERE account_id = $2 AND code_hash = $3 AND used_at IS NULL`,
		s.Now(), accountID, hashToken(code))
	if err != nil {
		return fmt.Errorf("consume recovery code: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count recovery code: %w", err)
	}
	if n != 1 {
		return ErrTOTPInvalid
	}
	return nil
}

// createLoginChallengeLocked locks the account (before any challenge row),
// re-checks the password, upgrades weaker hashes, then stores a short-lived
// challenge. Concurrent resets reject the login.
func (s *Service) createLoginChallengeLocked(ctx context.Context, accountID int64, verifiedHash, password string) (string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin login challenge: %w", err)
	}
	currentHash, err := s.lockAccountPassword(ctx, tx, accountID)
	if err != nil {
		return "", rollbackAuth(tx, err)
	}
	if err := s.ensurePasswordAndRehash(ctx, tx, accountID, currentHash, verifiedHash, password); err != nil {
		return "", rollbackAuth(tx, err)
	}
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_totp WHERE account_id = $1)`, accountID).Scan(&enabled)
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("check totp for challenge: %w", err))
	}
	if !enabled {
		return "", rollbackAuth(tx, ErrTOTPNotEnabled)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE account_id = $1`, accountID); err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("clear prior challenges: %w", err))
	}
	token, err := newToken()
	if err != nil {
		return "", rollbackAuth(tx, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO login_challenges (token_hash, account_id, expires_at)
		VALUES ($1, $2, $3)`, hashToken(token), accountID, s.Now().Add(challengeTTL))
	if err != nil {
		return "", rollbackAuth(tx, fmt.Errorf("insert login challenge: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit login challenge: %w", err)
	}
	return token, nil
}

func (s *Service) accountHasTOTP(ctx context.Context, accountID int64) (bool, error) {
	var enabled bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM account_totp WHERE account_id = $1)`, accountID).Scan(&enabled)
	if err != nil {
		return false, fmt.Errorf("check totp enabled: %w", err)
	}
	return enabled, nil
}

// DeleteExpiredTOTPState removes expired pending enrolments and login challenges.
func (s *Service) DeleteExpiredTOTPState(ctx context.Context) (int64, error) {
	now := s.Now()
	var total int64
	res, err := s.DB.ExecContext(ctx, `DELETE FROM totp_pending WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired totp pending: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n
	res, err = s.DB.ExecContext(ctx, `DELETE FROM login_challenges WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired login challenges: %w", err)
	}
	n, _ = res.RowsAffected()
	total += n
	return total, nil
}
