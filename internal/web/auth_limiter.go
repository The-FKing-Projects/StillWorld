package web

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	sharedLimitMaxKeys = 100_000
	sharedLimitPurge   = 64
)

const (
	// Scope values are stored persistently and must remain stable.
	limitLoginIP int16 = iota + 1
	limitLoginUser
	limitSignupIP
	limitRecoveryIP
	limitRecoveryEmail
	limitResetIP
)

var errLimitCapacity = errors.New("shared rate limiter capacity exhausted")

type authReservation struct {
	local      *entry
	digest     [32]byte
	generation int64
}

type authLimiter struct {
	local  *limiter
	db     *sql.DB
	scope  int16
	max    int
	window time.Duration
}

func newAuthLimiter(db *sql.DB, scope int16, max int, window time.Duration, now func() time.Time) *authLimiter {
	return &authLimiter{local: newLimiter(max, window, now), db: db, scope: scope, max: max, window: window}
}

func (l *authLimiter) reserve(ctx context.Context, key string) (*authReservation, error) {
	if l.db == nil {
		slot := l.local.reserve(key)
		if slot == nil {
			return nil, nil
		}
		return &authReservation{local: slot}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	digest := sha256.Sum256([]byte(key))
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin shared rate limit: %w", err)
	}
	defer tx.Rollback()
	// Serialize admissions and bounded cleanup across processes sharing the schema.
	var capacity int
	if err := tx.QueryRowContext(ctx, `SELECT entry_count FROM stillworld_rate_limit_capacity
		WHERE singleton FOR UPDATE`).Scan(&capacity); err != nil {
		return nil, fmt.Errorf("lock shared rate limit capacity: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM stillworld_rate_limits
		WHERE (scope, key_hash) IN (
			SELECT scope, key_hash FROM stillworld_rate_limits
			WHERE expires_at <= clock_timestamp() ORDER BY expires_at LIMIT $1
		)`, sharedLimitPurge)
	if err != nil {
		return nil, fmt.Errorf("purge shared rate limits: %w", err)
	}
	purged, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("count purged rate limits: %w", err)
	}
	if purged > 0 {
		capacity -= int(purged)
		if capacity < 0 {
			return nil, errors.New("shared rate limit capacity is inconsistent")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE stillworld_rate_limit_capacity
			SET entry_count = $1 WHERE singleton`, capacity); err != nil {
			return nil, fmt.Errorf("update shared rate limit capacity: %w", err)
		}
	}
	var generation int64
	var count, pending int
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT generation, failures, pending, expires_at > clock_timestamp()
		FROM stillworld_rate_limits WHERE scope = $1 AND key_hash = $2`, l.scope, digest[:]).
		Scan(&generation, &count, &pending, &active)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if capacity >= sharedLimitMaxKeys {
			return nil, errLimitCapacity
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO stillworld_rate_limits
			(scope, key_hash, failures, pending, expires_at)
			VALUES ($1, $2, 0, 1, clock_timestamp() + $3 * interval '1 second')
			RETURNING generation`, l.scope, digest[:], l.window.Seconds()).Scan(&generation)
		if err != nil {
			return nil, fmt.Errorf("create shared rate limit: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE stillworld_rate_limit_capacity
			SET entry_count = entry_count + 1 WHERE singleton`); err != nil {
			return nil, fmt.Errorf("reserve shared rate limit capacity: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("read shared rate limit: %w", err)
	case !active:
		err = tx.QueryRowContext(ctx, `UPDATE stillworld_rate_limits
			SET generation = nextval('stillworld_rate_limit_generation'), failures = 0, pending = 1,
				expires_at = clock_timestamp() + $3 * interval '1 second'
			WHERE scope = $1 AND key_hash = $2 RETURNING generation`,
			l.scope, digest[:], l.window.Seconds()).Scan(&generation)
		if err != nil {
			return nil, fmt.Errorf("renew shared rate limit: %w", err)
		}
	case count+pending >= l.max:
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit shared rate limit cleanup: %w", err)
		}
		return nil, nil
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE stillworld_rate_limits SET pending = pending + 1
			WHERE scope = $1 AND key_hash = $2`, l.scope, digest[:]); err != nil {
			return nil, fmt.Errorf("reserve shared rate limit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit shared rate limit reservation: %w", err)
	}
	return &authReservation{digest: digest, generation: generation}, nil
}

func (l *authLimiter) finish(slot *authReservation, failed, reset bool) error {
	if l.db == nil {
		l.local.finish(slot.local, failed, reset)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := l.db.ExecContext(ctx, `UPDATE stillworld_rate_limits SET
		pending = pending - 1,
		failures = CASE WHEN $4 THEN 0 WHEN $5 THEN failures + 1 ELSE failures END
		WHERE scope = $1 AND key_hash = $2 AND generation = $3 AND pending > 0`,
		l.scope, slot.digest[:], slot.generation, reset, failed)
	if err != nil {
		return fmt.Errorf("finish shared rate limit: %w", err)
	}
	return nil
}
