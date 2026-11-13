// Package actionengine executes registered actions with Turns and durable
// request outcomes in one PostgreSQL transaction.
package actionengine

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/internal/actiondata"
	"github.com/The-FKing-Projects/StillWorld/internal/actionregistry"
	"github.com/The-FKing-Projects/StillWorld/internal/actiontoken"
	"github.com/The-FKing-Projects/StillWorld/internal/turns"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	actionDeadline  = 10 * time.Second
	lockTimeout     = "5s"
	statementLimit  = "5s"
	outcomeVersion  = 1
	maxCleanupBatch = 1000
)

var (
	ErrInvalidRequestID = errors.New("action engine: invalid request ID")
	ErrExpiredRequestID = errors.New("action engine: request ID expired")
	ErrUnknownAction    = errors.New("action engine: unknown action")
	ErrUnknownAccount   = errors.New("action engine: unknown account")
	ErrRequestConflict  = errors.New("action engine: request ID conflicts with a prior request")
	ErrOutcomeUnknown   = errors.New("action engine: action outcome unknown; retry with the same request ID")
)

// RequestID contains a signed request token and its database-derived expiry.
type RequestID struct {
	Token     string
	ExpiresAt time.Time
}

// Outcome is a committed action success or game-rule rejection.
type Outcome struct {
	Version   int               `json:"version"`
	Status    string            `json:"status"`
	Result    json.RawMessage   `json:"result,omitempty"`
	Rejection *action.Rejection `json:"rejection,omitempty"`
}

// Engine owns action execution, Turns accounting, and request outcomes.
type Engine struct {
	db       *sql.DB
	registry *actionregistry.Registry
	turns    *turns.Service
	keys     *actiontoken.KeyRing
}

// New validates the game action registry and its execution configuration.
func New(
	db *sql.DB,
	gameID string,
	policy turns.Policy,
	keys *actiontoken.KeyRing,
	registrations []actionregistry.Registration,
) (*Engine, error) {
	if db == nil {
		return nil, errors.New("action engine: database is required")
	}
	if err := keys.Validate(); err != nil {
		return nil, fmt.Errorf("action engine: %w", err)
	}
	registry, err := actionregistry.New(gameID, registrations)
	if err != nil {
		return nil, err
	}
	turnService, err := turns.New(policy, nil)
	if err != nil {
		return nil, fmt.Errorf("action engine: %w", err)
	}
	return &Engine{db: db, registry: registry, turns: turnService, keys: keys}, nil
}

// IssueRequestID issues a token for an existing action using PostgreSQL time.
func (e *Engine) IssueRequestID(ctx context.Context, playerID int64, actionName string) (RequestID, error) {
	if e == nil || playerID <= 0 {
		return RequestID{}, ErrUnknownAccount
	}
	if _, ok := e.registry.Lookup(actionName); !ok {
		return RequestID{}, ErrUnknownAction
	}
	var now time.Time
	err := e.db.QueryRowContext(ctx,
		`SELECT clock_timestamp() FROM accounts WHERE id = $1`, playerID).Scan(&now)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestID{}, ErrUnknownAccount
	}
	if err != nil {
		return RequestID{}, fmt.Errorf("read database time: %w", err)
	}
	token, claims, err := e.keys.Issue(actiontoken.Scope{
		PlayerID:   playerID,
		GameID:     e.registry.GameID(),
		ActionName: actionName,
	}, now.UTC())
	if err != nil {
		return RequestID{}, fmt.Errorf("issue request ID: %w", err)
	}
	return RequestID{Token: token, ExpiresAt: claims.ExpiresAt}, nil
}

// CleanupExpired deletes expired outcomes in a bounded batch. It locks account
// rows in stable order before rechecking expiry and deleting their outcomes.
func (e *Engine) CleanupExpired(ctx context.Context, batchSize int) (int64, error) {
	if e == nil || e.db == nil {
		return 0, errors.New("action engine: database is required")
	}
	if batchSize < 1 || batchSize > maxCleanupBatch {
		return 0, fmt.Errorf("action engine: cleanup batch must be between 1 and %d", maxCleanupBatch)
	}
	ctx, cancel := context.WithTimeout(ctx, actionDeadline)
	defer cancel()

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin action outcome cleanup: %w", err)
	}
	if err := setActionTimeouts(ctx, tx); err != nil {
		return 0, rollback(tx, fmt.Errorf("set cleanup timeouts: %w", err))
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT a.id
		   FROM accounts a
		  WHERE EXISTS (
		        SELECT 1 FROM stillworld_action_outcomes o
		         WHERE o.account_id = a.id AND o.expires_at <= clock_timestamp()
		  )
		  ORDER BY a.id
		  LIMIT $1
		  FOR UPDATE OF a SKIP LOCKED`, batchSize)
	if err != nil {
		return 0, rollback(tx, fmt.Errorf("lock expired outcome accounts: %w", err))
	}
	accountIDs := make([]int64, 0, batchSize)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			_ = rows.Close()
			return 0, rollback(tx, fmt.Errorf("scan expired outcome account: %w", err))
		}
		accountIDs = append(accountIDs, accountID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, rollback(tx, fmt.Errorf("iterate expired outcome accounts: %w", err))
	}
	if err := rows.Close(); err != nil {
		return 0, rollback(tx, fmt.Errorf("close expired outcome account rows: %w", err))
	}
	var deleted int64
	for _, accountID := range accountIDs {
		result, err := tx.ExecContext(ctx,
			`DELETE FROM stillworld_action_outcomes
			  WHERE account_id = $1 AND expires_at <= clock_timestamp()`, accountID)
		if err != nil {
			return 0, rollback(tx, fmt.Errorf("delete expired action outcomes: %w", err))
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, rollback(tx, fmt.Errorf("count deleted action outcomes: %w", err))
		}
		deleted += count
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit action outcome cleanup: %w", err)
	}
	return deleted, nil
}

// Execute runs an authenticated action. The caller must take playerID from
// the framework's authenticated request context, never from request data.
func (e *Engine) Execute(
	ctx context.Context,
	playerID int64,
	actionName string,
	token string,
	input []byte,
) (Outcome, error) {
	if e == nil || playerID <= 0 {
		return Outcome{}, ErrUnknownAccount
	}
	if err := actiondata.ValidateInput(input); err != nil {
		return Outcome{}, fmt.Errorf("action engine: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, actionDeadline)
	defer cancel()
	now, err := e.databaseTime(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("read database time: %w", err)
	}
	scope := actiontoken.Scope{
		PlayerID:   playerID,
		GameID:     e.registry.GameID(),
		ActionName: actionName,
	}
	claims, err := e.keys.Verify(token, now, scope)
	if errors.Is(err, actiontoken.ErrExpiredToken) {
		return Outcome{}, ErrExpiredRequestID
	}
	if err != nil {
		return Outcome{}, ErrInvalidRequestID
	}
	fingerprint := actiondata.Fingerprint(actionName, input)

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Outcome{}, fmt.Errorf("begin action transaction: %w", err)
	}
	if err := setActionTimeouts(ctx, tx); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("set action timeouts: %w", err))
	}

	var lockedAccountID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, playerID).Scan(&lockedAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return Outcome{}, rollback(tx, ErrUnknownAccount)
	}
	if err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("lock action account: %w", err))
	}

	now, err = transactionTime(ctx, tx)
	if err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("recheck request expiry: %w", err))
	}
	if !now.Before(claims.ExpiresAt) {
		return Outcome{}, rollback(tx, ErrExpiredRequestID)
	}

	outcome, found, err := loadOutcome(ctx, tx, playerID, claims.Nonce)
	if err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("load action outcome: %w", err))
	}
	if found {
		if outcome.actionName != actionName || !bytes.Equal(outcome.fingerprint, fingerprint[:]) ||
			!outcome.expiresAt.Equal(claims.ExpiresAt) {
			return Outcome{}, rollback(tx, ErrRequestConflict)
		}
		if err := tx.Commit(); err != nil {
			return Outcome{}, fmt.Errorf("finish duplicate action transaction: %w", err)
		}
		return outcome.value, nil
	}

	handler, ok := e.registry.Lookup(actionName)
	if !ok {
		return Outcome{}, rollback(tx, ErrUnknownAction)
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT stillworld_action_work`); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("create action savepoint: %w", err))
	}
	if _, err := e.turns.Current(ctx, tx, playerID); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("materialize action Turns: %w", err))
	}

	handlerDB := transactionDB{tx: tx}
	prepared, err := handler.Prepare(ctx, action.Player{ID: playerID}, handlerDB,
		append(json.RawMessage(nil), input...))
	if err != nil {
		if rejection := ruleRejection(err); rejection != nil {
			return e.commitRejection(ctx, tx, playerID, actionName, claims, fingerprint, rejection)
		}
		return Outcome{}, rollback(tx, fmt.Errorf("prepare action %q: %w", actionName, err))
	}
	if prepared.Cost < 0 || prepared.Apply == nil {
		return Outcome{}, rollback(tx, fmt.Errorf("action %q returned invalid prepared state", actionName))
	}
	if _, err := e.turns.Spend(ctx, tx, playerID, prepared.Cost); err != nil {
		if errors.Is(err, turns.ErrInsufficient) {
			rejection := &action.Rejection{
				Code:    "stillworld.insufficient_turns",
				Message: "Not enough Turns.",
			}
			return e.commitRejection(ctx, tx, playerID, actionName, claims, fingerprint, rejection)
		}
		return Outcome{}, rollback(tx, fmt.Errorf("spend action Turns: %w", err))
	}

	result, err := prepared.Apply(ctx, handlerDB)
	if err != nil {
		if rejection := ruleRejection(err); rejection != nil {
			return e.commitRejection(ctx, tx, playerID, actionName, claims, fingerprint, rejection)
		}
		return Outcome{}, rollback(tx, fmt.Errorf("apply action %q: %w", actionName, err))
	}
	if err := actiondata.ValidateResult(result); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("validate action result: %w", err))
	}

	value := Outcome{Version: outcomeVersion, Status: "success", Result: append(json.RawMessage(nil), result...)}
	if err := insertOutcome(ctx, tx, playerID, actionName, claims, fingerprint, value); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("record action success: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	return value, nil
}

func (e *Engine) commitRejection(
	ctx context.Context,
	tx *sql.Tx,
	playerID int64,
	actionName string,
	claims actiontoken.Claims,
	fingerprint [32]byte,
	rejection *action.Rejection,
) (Outcome, error) {
	if err := actiondata.ValidateRejection(rejection); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("validate action rejection: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT stillworld_action_work`); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("rollback rejected action: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT stillworld_action_work`); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("release rejected action savepoint: %w", err))
	}
	value := Outcome{
		Version:   outcomeVersion,
		Status:    "rejected",
		Rejection: &action.Rejection{Code: rejection.Code, Message: rejection.Message},
	}
	if err := insertOutcome(ctx, tx, playerID, actionName, claims, fingerprint, value); err != nil {
		return Outcome{}, rollback(tx, fmt.Errorf("record action rejection: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	return value, nil
}

type storedOutcome struct {
	actionName  string
	fingerprint []byte
	expiresAt   time.Time
	value       Outcome
}

func loadOutcome(ctx context.Context, tx *sql.Tx, playerID int64, nonce [16]byte) (storedOutcome, bool, error) {
	var stored storedOutcome
	var status string
	var result []byte
	var rejectionCode, rejectionMessage sql.NullString
	var version int
	err := tx.QueryRowContext(ctx,
		`SELECT action_name, input_fingerprint, expires_at, outcome_version, status,
		        result_json, rejection_code, rejection_message
		   FROM stillworld_action_outcomes
		  WHERE account_id = $1 AND nonce = $2`,
		playerID, nonce[:]).Scan(
		&stored.actionName, &stored.fingerprint, &stored.expiresAt, &version, &status,
		&result, &rejectionCode, &rejectionMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return storedOutcome{}, false, nil
	}
	if err != nil {
		return storedOutcome{}, false, err
	}
	if version != outcomeVersion {
		return storedOutcome{}, false, fmt.Errorf("unsupported action outcome version %d", version)
	}
	stored.expiresAt = stored.expiresAt.UTC()
	switch status {
	case "success":
		if rejectionCode.Valid || rejectionMessage.Valid ||
			actiondata.ValidateResult(result) != nil {
			return storedOutcome{}, false, errors.New("invalid stored action success")
		}
		stored.value = Outcome{
			Version: outcomeVersion,
			Status:  "success",
			Result:  append(json.RawMessage(nil), result...),
		}
	case "rejected":
		if result != nil || !rejectionCode.Valid || !rejectionMessage.Valid {
			return storedOutcome{}, false, errors.New("invalid stored action rejection")
		}
		rejection := &action.Rejection{Code: rejectionCode.String, Message: rejectionMessage.String}
		if err := actiondata.ValidateRejection(rejection); err != nil {
			return storedOutcome{}, false, fmt.Errorf("invalid stored action rejection: %w", err)
		}
		stored.value = Outcome{Version: outcomeVersion, Status: "rejected", Rejection: rejection}
	default:
		return storedOutcome{}, false, fmt.Errorf("invalid stored action status %q", status)
	}
	return stored, true, nil
}

func insertOutcome(
	ctx context.Context,
	tx *sql.Tx,
	playerID int64,
	actionName string,
	claims actiontoken.Claims,
	fingerprint [32]byte,
	outcome Outcome,
) error {
	var result any
	var rejectionCode, rejectionMessage any
	if outcome.Status == "success" {
		result = []byte(outcome.Result)
	} else {
		rejectionCode = outcome.Rejection.Code
		rejectionMessage = outcome.Rejection.Message
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO stillworld_action_outcomes
		     (account_id, nonce, action_name, input_fingerprint, expires_at,
		      outcome_version, status, result_json, rejection_code, rejection_message)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		playerID, claims.Nonce[:], actionName, fingerprint[:], claims.ExpiresAt,
		outcomeVersion, outcome.Status, result, rejectionCode, rejectionMessage)
	return err
}

func (e *Engine) databaseTime(ctx context.Context) (time.Time, error) {
	var now time.Time
	if err := e.db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}

func transactionTime(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, err
	}
	return now.UTC(), nil
}

func setActionTimeouts(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '`+lockTimeout+`'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '`+statementLimit+`'`); err != nil {
		return err
	}
	return nil
}

func rollback(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return errors.Join(cause, fmt.Errorf("roll back action transaction: %w", err))
	}
	return cause
}

func ruleRejection(err error) *action.Rejection {
	var rejection *action.Rejection
	if !errors.As(err, &rejection) || rejection == nil || isTechnicalFailure(err) {
		return nil
	}
	return rejection
}

func isTechnicalFailure(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) ||
		errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

type transactionDB struct {
	tx *sql.Tx
}

func (d transactionDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.tx.ExecContext(ctx, query, args...)
}

func (d transactionDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.tx.QueryContext(ctx, query, args...)
}

func (d transactionDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.tx.QueryRowContext(ctx, query, args...)
}
