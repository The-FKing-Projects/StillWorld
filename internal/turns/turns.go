// Package turns provides on-demand regeneration and transaction-scoped spending.
package turns

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"time"
)

var (
	ErrInvalidPolicy = errors.New("turns: invalid policy")
	ErrInvalidCost   = errors.New("turns: cost must be nonnegative")
	ErrInsufficient  = errors.New("turns: insufficient balance")
	ErrInvalidState  = errors.New("turns: invalid stored state")
)

// Policy is game-owned configuration. Interval is the time to earn one Turn.
// Cap must be positive; Interval must be a positive whole number of microseconds.
type Policy struct {
	Cap      int64
	Interval time.Duration
}

// Service applies an immutable policy and a clock sampled only after row locking.
type Service struct {
	policy Policy
	now    func() time.Time
}

// New validates policy. A nil clock uses time.Now.
func New(policy Policy, clock func() time.Time) (*Service, error) {
	if policy.Cap <= 0 || policy.Interval < time.Microsecond || policy.Interval%time.Microsecond != 0 {
		return nil, ErrInvalidPolicy
	}
	if clock == nil {
		clock = time.Now
	}
	return &Service{policy: policy, now: clock}, nil
}

// Current locks the account and materializes regeneration, returning spendable
// whole Turns. The caller owns tx and must commit or roll back to release its lock.
func (s *Service) Current(ctx context.Context, tx *sql.Tx, accountID int64) (int64, error) {
	return s.Spend(ctx, tx, accountID, 0)
}

// Spend locks the account before sampling time, regenerates and spends cost,
// returning the remaining whole Turns. It never commits or rolls back tx.
// Invalid or insufficient spends do not update the row. On any error the caller
// must roll back the action transaction (including any game effects).
func (s *Service) Spend(ctx context.Context, tx *sql.Tx, accountID, cost int64) (int64, error) {
	if cost < 0 {
		return 0, ErrInvalidCost
	}
	var balance string
	var updated time.Time
	if err := tx.QueryRowContext(ctx,
		`SELECT turns_balance::text, turns_updated_at FROM accounts WHERE id = $1 FOR UPDATE`,
		accountID).Scan(&balance, &updated); err != nil {
		return 0, fmt.Errorf("lock Turns account: %w", err)
	}
	state, err := s.calculate(balance, updated, s.now().UTC().Truncate(time.Microsecond))
	if err != nil {
		return 0, err
	}
	if cost > state.available {
		return 0, ErrInsufficient
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE accounts
		    SET turns_balance = LEAST(turns_balance + $2::numeric, $3::numeric) - $4::numeric,
		        turns_updated_at = $5
		  WHERE id = $1`,
		accountID, state.earned, s.policy.Cap, cost, state.updated); err != nil {
		return 0, fmt.Errorf("update Turns: %w", err)
	}
	return state.available - cost, nil
}

type accrual struct {
	available int64
	earned    int64
	updated   time.Time
}

func (s *Service) calculate(text string, updated, now time.Time) (accrual, error) {
	balance, ok := new(big.Rat).SetString(text)
	if !ok || balance.Sign() < 0 {
		return accrual{}, ErrInvalidState
	}
	// NUMERIC and elapsed times may exceed int64. Bound by the cap before
	// converting, rather than overflowing or using floating-point arithmetic.
	earned, remainder := new(big.Int), new(big.Int)
	if now.After(updated) {
		elapsed := new(big.Int).Sub(microseconds(now), microseconds(updated))
		earned.QuoRem(elapsed, big.NewInt(s.policy.Interval.Microseconds()), remainder)
	}
	current := new(big.Rat).Add(balance, new(big.Rat).SetInt(earned))
	if current.Cmp(new(big.Rat).SetInt64(s.policy.Cap)) >= 0 {
		if updated.After(now) {
			now = updated
		}
		return accrual{s.policy.Cap, s.policy.Cap, now}, nil
	}
	whole := new(big.Int).Quo(current.Num(), current.Denom()).Int64()
	if now.After(updated) {
		updated = now.Add(-time.Duration(remainder.Int64()) * time.Microsecond)
	}
	return accrual{whole, earned.Int64(), updated}, nil
}

func microseconds(t time.Time) *big.Int {
	value := new(big.Int).Mul(big.NewInt(t.Unix()), big.NewInt(1_000_000))
	return value.Add(value, big.NewInt(int64(t.Nanosecond()/1_000)))
}
