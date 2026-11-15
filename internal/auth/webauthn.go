package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	webauthnCeremonyTTL = 5 * time.Minute
	maxPasskeyLabelLen  = 64
)

var (
	// ErrWebAuthnUnavailable means WebAuthn RP settings are not configured.
	ErrWebAuthnUnavailable = errors.New("auth: webauthn unavailable")
	// ErrWebAuthnInvalid means a ceremony response was rejected.
	ErrWebAuthnInvalid = errors.New("auth: invalid webauthn response")
	// ErrWebAuthnNotEnabled means the account has no passkeys.
	ErrWebAuthnNotEnabled = errors.New("auth: webauthn not enabled")
	// ErrWebAuthnCredentialMissing means the credential id was not found.
	ErrWebAuthnCredentialMissing = errors.New("auth: webauthn credential not found")
)

// WebAuthnConfig is the Relying Party settings for optional passkeys.
type WebAuthnConfig struct {
	RPID          string
	RPDisplayName string
	RPOrigins     []string
}

// PasskeyCredential is a stored public credential summary for management UI.
type PasskeyCredential struct {
	ID           int64
	CredentialID []byte
	Label        string
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

// SecondFactorInfo reports which second factors can complete a login challenge.
type SecondFactorInfo struct {
	TOTP    bool
	Passkey bool
}

// SetWebAuthn configures the Relying Party used for optional passkeys.
// A nil config disables enrolment and passkey login completion.
func (s *Service) SetWebAuthn(cfg *WebAuthnConfig) error {
	if cfg == nil {
		s.wa = nil
		return nil
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     append([]string(nil), cfg.RPOrigins...),
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementDiscouraged,
			RequireResidentKey: protocol.ResidentKeyNotRequired(),
			UserVerification:   protocol.VerificationPreferred,
		},
		AttestationPreference: protocol.PreferNoAttestation,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebAuthnUnavailable, err)
	}
	s.wa = wa
	return nil
}

// BeginPasskeyRegistration starts a WebAuthn registration ceremony for a signed-in account.
// The returned creation options are JSON for the browser; session state is stored server-side.
func (s *Service) BeginPasskeyRegistration(ctx context.Context, accountID int64, username string) ([]byte, error) {
	if s.wa == nil {
		return nil, ErrWebAuthnUnavailable
	}
	if accountID <= 0 || username == "" {
		return nil, ErrInvalidInput
	}
	user, err := s.loadWebAuthnUser(ctx, accountID, username)
	if err != nil {
		return nil, err
	}
	creation, session, err := s.wa.BeginRegistration(user,
		webauthn.WithExclusions(webauthn.Credentials(user.WebAuthnCredentials()).CredentialDescriptors()),
		webauthn.WithExtensions(webauthn.WithExtensionCredProps()),
	)
	if err != nil {
		return nil, fmt.Errorf("begin passkey registration: %w", err)
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("encode registration session: %w", err)
	}
	expires := s.Now().Add(webauthnCeremonyTTL)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO webauthn_register_pending (account_id, session_data, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id) DO UPDATE SET
			session_data = EXCLUDED.session_data,
			expires_at = EXCLUDED.expires_at`,
		accountID, sessionJSON, expires)
	if err != nil {
		return nil, fmt.Errorf("store registration session: %w", err)
	}
	optionsJSON, err := json.Marshal(creation)
	if err != nil {
		return nil, fmt.Errorf("encode creation options: %w", err)
	}
	return optionsJSON, nil
}

// FinishPasskeyRegistration completes registration from the browser attestation response body.
func (s *Service) FinishPasskeyRegistration(ctx context.Context, accountID int64, username string, body []byte, label string) error {
	if s.wa == nil {
		return ErrWebAuthnUnavailable
	}
	label = strings.TrimSpace(label)
	if len(label) > maxPasskeyLabelLen {
		return fmt.Errorf("%w: label too long", ErrInvalidInput)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin passkey finish: %w", err)
	}
	var sessionJSON []byte
	var expires sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT session_data, expires_at FROM webauthn_register_pending
		WHERE account_id = $1 FOR UPDATE`, accountID).Scan(&sessionJSON, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrWebAuthnInvalid)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("load registration session: %w", err))
	}
	now := s.Now()
	if !expires.Valid || !expires.Time.After(now) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM webauthn_register_pending WHERE account_id = $1`, accountID)
		return rollbackAuth(tx, ErrWebAuthnInvalid)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(sessionJSON, &session); err != nil {
		return rollbackAuth(tx, fmt.Errorf("decode registration session: %w", err))
	}
	user, err := s.loadWebAuthnUserTx(ctx, tx, accountID, username)
	if err != nil {
		return rollbackAuth(tx, err)
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("%w: %v", ErrWebAuthnInvalid, err))
	}
	credential, err := s.wa.CreateCredential(user, session, parsed)
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("%w: %v", ErrWebAuthnInvalid, err))
	}
	credJSON, err := json.Marshal(credential)
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("encode credential: %w", err))
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO webauthn_credentials
		(account_id, credential_id, credential_json, label, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		accountID, credential.ID, credJSON, label, now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return rollbackAuth(tx, ErrWebAuthnInvalid)
		}
		return rollbackAuth(tx, fmt.Errorf("store credential: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM webauthn_register_pending WHERE account_id = $1`, accountID); err != nil {
		return rollbackAuth(tx, fmt.Errorf("clear registration session: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit passkey registration: %w", err)
	}
	return nil
}

// BeginPasskeyLogin starts an assertion ceremony for a password-accepted login challenge.
func (s *Service) BeginPasskeyLogin(ctx context.Context, challengeToken string) ([]byte, error) {
	if s.wa == nil {
		return nil, ErrWebAuthnUnavailable
	}
	if challengeToken == "" {
		return nil, ErrTOTPChallenge
	}
	challengeHash := hashToken(challengeToken)
	now := s.Now()
	var accountID int64
	var username string
	err := s.DB.QueryRowContext(ctx, `SELECT c.account_id, a.username
		FROM login_challenges c
		JOIN accounts a ON a.id = c.account_id
		WHERE c.token_hash = $1 AND c.expires_at > $2 AND a.disabled_at IS NULL`,
		challengeHash, now).Scan(&accountID, &username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTOTPChallenge
	}
	if err != nil {
		return nil, fmt.Errorf("load login challenge for passkey: %w", err)
	}
	user, err := s.loadWebAuthnUser(ctx, accountID, username)
	if err != nil {
		return nil, err
	}
	if len(user.creds) == 0 {
		return nil, ErrWebAuthnNotEnabled
	}
	assertion, session, err := s.wa.BeginLogin(user)
	if err != nil {
		return nil, fmt.Errorf("begin passkey login: %w", err)
	}
	sessionJSON, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("encode assertion session: %w", err)
	}
	expires := s.Now().Add(webauthnCeremonyTTL)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO webauthn_assert_pending
		(login_challenge_hash, account_id, session_data, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (login_challenge_hash) DO UPDATE SET
			account_id = EXCLUDED.account_id,
			session_data = EXCLUDED.session_data,
			expires_at = EXCLUDED.expires_at`,
		challengeHash, accountID, sessionJSON, expires)
	if err != nil {
		return nil, fmt.Errorf("store assertion session: %w", err)
	}
	optionsJSON, err := json.Marshal(assertion)
	if err != nil {
		return nil, fmt.Errorf("encode assertion options: %w", err)
	}
	return optionsJSON, nil
}

// CompleteLoginPasskey finishes a password-accepted login with a WebAuthn assertion.
func (s *Service) CompleteLoginPasskey(ctx context.Context, challengeToken string, body []byte) (string, error) {
	if s.wa == nil {
		return "", ErrWebAuthnUnavailable
	}
	return s.completeChallenge(ctx, challengeToken, func(tx *sql.Tx, accountID int64) error {
		return s.verifyAndConsumePasskey(ctx, tx, accountID, challengeToken, body)
	})
}

func (s *Service) verifyAndConsumePasskey(ctx context.Context, tx *sql.Tx, accountID int64, challengeToken string, body []byte) error {
	challengeHash := hashToken(challengeToken)
	var sessionJSON []byte
	var expires sql.NullTime
	var pendingAccount int64
	err := tx.QueryRowContext(ctx, `SELECT account_id, session_data, expires_at
		FROM webauthn_assert_pending WHERE login_challenge_hash = $1 FOR UPDATE`,
		challengeHash).Scan(&pendingAccount, &sessionJSON, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrWebAuthnInvalid
	}
	if err != nil {
		return fmt.Errorf("load assertion session: %w", err)
	}
	if pendingAccount != accountID {
		return ErrWebAuthnInvalid
	}
	now := s.Now()
	if !expires.Valid || !expires.Time.After(now) {
		_, _ = tx.ExecContext(ctx, `DELETE FROM webauthn_assert_pending WHERE login_challenge_hash = $1`, challengeHash)
		return ErrWebAuthnInvalid
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(sessionJSON, &session); err != nil {
		return fmt.Errorf("decode assertion session: %w", err)
	}
	var username string
	err = tx.QueryRowContext(ctx, `SELECT username FROM accounts WHERE id = $1`, accountID).Scan(&username)
	if err != nil {
		return fmt.Errorf("load username for passkey: %w", err)
	}
	user, err := s.loadWebAuthnUserTx(ctx, tx, accountID, username)
	if err != nil {
		return err
	}
	if len(user.creds) == 0 {
		return ErrWebAuthnNotEnabled
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebAuthnInvalid, err)
	}
	credential, err := s.wa.ValidateLogin(user, session, parsed)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWebAuthnInvalid, err)
	}
	credJSON, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode updated credential: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE webauthn_credentials
		SET credential_json = $1, last_used_at = $2
		WHERE account_id = $3 AND credential_id = $4`,
		credJSON, now, accountID, credential.ID)
	if err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("count credential update: %w", err)
	}
	if n != 1 {
		return ErrWebAuthnInvalid
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM webauthn_assert_pending WHERE login_challenge_hash = $1`, challengeHash); err != nil {
		return fmt.Errorf("clear assertion session: %w", err)
	}
	return nil
}

// ListPasskeys returns stored credentials for an account.
func (s *Service) ListPasskeys(ctx context.Context, accountID int64) ([]PasskeyCredential, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, credential_id, label, created_at, last_used_at
		FROM webauthn_credentials WHERE account_id = $1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list passkeys: %w", err)
	}
	defer rows.Close()
	var out []PasskeyCredential
	for rows.Next() {
		var c PasskeyCredential
		var last sql.NullTime
		if err := rows.Scan(&c.ID, &c.CredentialID, &c.Label, &c.CreatedAt, &last); err != nil {
			return nil, fmt.Errorf("scan passkey: %w", err)
		}
		if last.Valid {
			t := last.Time
			c.LastUsedAt = &t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeletePasskey removes one credential after verifying the account password.
func (s *Service) DeletePasskey(ctx context.Context, accountID int64, credentialRowID int64, password string) error {
	if len(password) > maxPasswordBytes {
		return ErrInvalidCredentials
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin passkey delete: %w", err)
	}
	var hash string
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM accounts
		WHERE id = $1 AND disabled_at IS NULL FOR UPDATE`, accountID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackAuth(tx, ErrAccountNotFound)
	}
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("lock account for passkey delete: %w", err))
	}
	ok, verr := VerifyPassword(password, hash)
	if verr != nil {
		return rollbackAuth(tx, fmt.Errorf("verify password for passkey delete: %w", verr))
	}
	if !ok {
		return rollbackAuth(tx, ErrInvalidCredentials)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM webauthn_credentials
		WHERE id = $1 AND account_id = $2`, credentialRowID, accountID)
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("delete passkey: %w", err))
	}
	n, err := res.RowsAffected()
	if err != nil {
		return rollbackAuth(tx, fmt.Errorf("count passkey delete: %w", err))
	}
	if n == 0 {
		return rollbackAuth(tx, ErrWebAuthnCredentialMissing)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit passkey delete: %w", err)
	}
	return nil
}

// LoginChallengeFactors reports which second factors can finish a challenge.
func (s *Service) LoginChallengeFactors(ctx context.Context, challengeToken string) (SecondFactorInfo, error) {
	if challengeToken == "" {
		return SecondFactorInfo{}, ErrTOTPChallenge
	}
	var accountID int64
	err := s.DB.QueryRowContext(ctx, `SELECT account_id FROM login_challenges
		WHERE token_hash = $1 AND expires_at > $2`,
		hashToken(challengeToken), s.Now()).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return SecondFactorInfo{}, ErrTOTPChallenge
	}
	if err != nil {
		return SecondFactorInfo{}, fmt.Errorf("load challenge factors: %w", err)
	}
	hasTOTP, err := s.accountHasTOTP(ctx, accountID)
	if err != nil {
		return SecondFactorInfo{}, err
	}
	hasPasskey, err := s.accountHasPasskey(ctx, accountID)
	if err != nil {
		return SecondFactorInfo{}, err
	}
	return SecondFactorInfo{
		TOTP:    hasTOTP && s.totp != nil,
		Passkey: hasPasskey && s.wa != nil,
	}, nil
}

func (s *Service) accountHasPasskey(ctx context.Context, accountID int64) (bool, error) {
	var enabled bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM webauthn_credentials WHERE account_id = $1)`, accountID).Scan(&enabled)
	if err != nil {
		return false, fmt.Errorf("check passkey enabled: %w", err)
	}
	return enabled, nil
}

func (s *Service) accountHasSecondFactor(ctx context.Context, accountID int64) (hasTOTP, hasPasskey bool, err error) {
	hasTOTP, err = s.accountHasTOTP(ctx, accountID)
	if err != nil {
		return false, false, err
	}
	hasPasskey, err = s.accountHasPasskey(ctx, accountID)
	if err != nil {
		return false, false, err
	}
	return hasTOTP, hasPasskey, nil
}

// DeleteExpiredWebAuthnState removes expired registration and assertion sessions.
func (s *Service) DeleteExpiredWebAuthnState(ctx context.Context) (int64, error) {
	now := s.Now()
	var total int64
	res, err := s.DB.ExecContext(ctx, `DELETE FROM webauthn_register_pending WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired webauthn register pending: %w", err)
	}
	n, _ := res.RowsAffected()
	total += n
	res, err = s.DB.ExecContext(ctx, `DELETE FROM webauthn_assert_pending WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired webauthn assert pending: %w", err)
	}
	n, _ = res.RowsAffected()
	total += n
	return total, nil
}

type webAuthnUser struct {
	id    []byte
	name  string
	creds []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte                         { return u.id }
func (u *webAuthnUser) WebAuthnName() string                       { return u.name }
func (u *webAuthnUser) WebAuthnDisplayName() string                { return u.name }
func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func webAuthnUserHandle(accountID int64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(accountID))
	return buf
}

func (s *Service) loadWebAuthnUser(ctx context.Context, accountID int64, username string) (*webAuthnUser, error) {
	creds, err := s.loadCredentials(ctx, s.DB, accountID)
	if err != nil {
		return nil, err
	}
	return &webAuthnUser{id: webAuthnUserHandle(accountID), name: username, creds: creds}, nil
}

func (s *Service) loadWebAuthnUserTx(ctx context.Context, tx *sql.Tx, accountID int64, username string) (*webAuthnUser, error) {
	creds, err := s.loadCredentials(ctx, tx, accountID)
	if err != nil {
		return nil, err
	}
	return &webAuthnUser{id: webAuthnUserHandle(accountID), name: username, creds: creds}, nil
}

type queryable interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Service) loadCredentials(ctx context.Context, q queryable, accountID int64) ([]webauthn.Credential, error) {
	rows, err := q.QueryContext(ctx, `SELECT credential_json FROM webauthn_credentials WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	defer rows.Close()
	var creds []webauthn.Credential
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		var c webauthn.Credential
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("decode credential: %w", err)
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}
