package actionengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/internal/actionregistry"
	"github.com/The-FKing-Projects/StillWorld/internal/actiontoken"
	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/The-FKing-Projects/StillWorld/internal/turns"
)

type actionFunc func(context.Context, action.Player, action.DB, json.RawMessage) (action.Prepared, error)

func (f actionFunc) Prepare(ctx context.Context, player action.Player, db action.DB, input json.RawMessage) (action.Prepared, error) {
	return f(ctx, player, db, input)
}

func TestExecuteAndReplaySuccess(t *testing.T) {
	d, playerID := setupAccount(t, 5)
	createCounterTable(t, d)
	var applyCalls atomic.Int32
	handler := incrementAction(&applyCalls, 0, 0)
	engine := newTestEngine(t, d, handler)
	requestID := issue(t, engine, playerID, "counter.increment")

	input := []byte(`{"amount":2}`)
	first, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "success" || second.Status != "success" || string(first.Result) != string(second.Result) {
		t.Fatalf("outcomes differ: first=%+v second=%+v", first, second)
	}
	if applyCalls.Load() != 1 {
		t.Fatalf("Apply called %d times, want 1", applyCalls.Load())
	}
	if got := counterValue(t, d, playerID); got != 2 {
		t.Fatalf("counter = %d, want 2", got)
	}
	if got := turnsBalance(t, d, playerID); got != 3 {
		t.Fatalf("Turns balance = %d, want 3", got)
	}
}

func TestInsufficientTurnsRejectionIsFinal(t *testing.T) {
	d, playerID := setupAccount(t, 1)
	var applyCalls atomic.Int32
	handler := incrementAction(&applyCalls, 0, 0)
	engine := newTestEngine(t, d, handler)
	requestID := issue(t, engine, playerID, "counter.increment")
	input := []byte(`{"amount":3}`)

	first, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "rejected" || first.Rejection == nil || first.Rejection.Code != "stillworld.insufficient_turns" {
		t.Fatalf("outcome = %+v, want insufficient-Turns rejection", first)
	}
	if _, err := d.ExecContext(context.Background(),
		`UPDATE accounts SET turns_balance = 5, turns_updated_at = clock_timestamp() WHERE id = $1`, playerID); err != nil {
		t.Fatal(err)
	}
	second, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "rejected" || second.Rejection == nil ||
		second.Rejection.Code != "stillworld.insufficient_turns" {
		t.Fatalf("retry outcome = %+v, want same final rejection", second)
	}
	if applyCalls.Load() != 0 {
		t.Fatalf("Apply called %d times, want 0", applyCalls.Load())
	}
	if got := turnsBalance(t, d, playerID); got != 5 {
		t.Fatalf("Turns balance = %d, want 5", got)
	}
}

func TestApplyRejectionRollsBackGameAndTurnsChanges(t *testing.T) {
	d, playerID := setupAccount(t, 5)
	createCounterTable(t, d)
	var applyCalls atomic.Int32
	handler := incrementAction(&applyCalls, 0, 1)
	engine := newTestEngine(t, d, handler)
	requestID := issue(t, engine, playerID, "counter.increment")

	outcome, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, []byte(`{"amount":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "rejected" || outcome.Rejection == nil || outcome.Rejection.Code != "counter.denied" {
		t.Fatalf("outcome = %+v, want counter rejection", outcome)
	}
	if got := counterRowCount(t, d, playerID); got != 0 {
		t.Fatalf("counter rows = %d, want 0 after rejection rollback", got)
	}
	if got := turnsBalance(t, d, playerID); got != 5 {
		t.Fatalf("Turns balance = %d, want 5 after rejection rollback", got)
	}
}

func TestTechnicalFailureRollsBackAndCanRetrySameRequestID(t *testing.T) {
	d, playerID := setupAccount(t, 5)
	createCounterTable(t, d)
	var applyCalls atomic.Int32
	handler := incrementAction(&applyCalls, 1, 0)
	engine := newTestEngine(t, d, handler)
	requestID := issue(t, engine, playerID, "counter.increment")
	input := []byte(`{"amount":1}`)

	if _, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, input); err == nil {
		t.Fatal("first attempt unexpectedly succeeded")
	}
	if got := counterRowCount(t, d, playerID); got != 0 {
		t.Fatalf("counter rows = %d after technical failure, want 0", got)
	}
	if got := turnsBalance(t, d, playerID); got != 5 {
		t.Fatalf("Turns balance = %d after technical failure, want 5", got)
	}

	outcome, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, input)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != "success" || applyCalls.Load() != 2 {
		t.Fatalf("retry outcome=%+v, Apply calls=%d", outcome, applyCalls.Load())
	}
}

func TestConcurrentDuplicateSubmissionsExecuteOnce(t *testing.T) {
	d, playerID := setupAccount(t, 5)
	createCounterTable(t, d)
	var applyCalls atomic.Int32
	handler := incrementAction(&applyCalls, 0, 0)
	engine := newTestEngine(t, d, handler)
	requestID := issue(t, engine, playerID, "counter.increment")
	input := []byte(`{"amount":1}`)

	results := make([]Outcome, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, input)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Execute %d: %v", i, err)
		}
		if results[i].Status != "success" {
			t.Fatalf("Execute %d outcome = %+v", i, results[i])
		}
	}
	if applyCalls.Load() != 1 {
		t.Fatalf("Apply called %d times, want 1", applyCalls.Load())
	}
	if got := counterValue(t, d, playerID); got != 1 {
		t.Fatalf("counter = %d, want 1", got)
	}
}

func TestCleanupExpiredOutcomesIsBoundedAndSkipsLockedAccounts(t *testing.T) {
	d, firstPlayer := setupAccount(t, 5)
	var secondPlayer int64
	secondUsername := fmt.Sprintf("action_%x", sha256.Sum256([]byte("cleanup-second")))[:24]
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO accounts (username, password_hash, turns_balance, turns_updated_at)
		 VALUES ($1, 'test', 5, clock_timestamp()) RETURNING id`, secondUsername).Scan(&secondPlayer); err != nil {
		t.Fatal(err)
	}
	createCounterTable(t, d)
	engine := newTestEngine(t, d, incrementAction(&atomic.Int32{}, 0, 0))
	for _, playerID := range []int64{firstPlayer, secondPlayer} {
		requestID := issue(t, engine, playerID, "counter.increment")
		if _, err := engine.Execute(context.Background(), playerID, "counter.increment", requestID.Token, []byte(`{"amount":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ExecContext(context.Background(),
		`UPDATE stillworld_action_outcomes
		    SET expires_at = clock_timestamp() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}

	lockTx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var lockedID int64
	if err := lockTx.QueryRowContext(context.Background(),
		`SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, firstPlayer).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	deleted, err := engine.CleanupExpired(context.Background(), 1)
	if err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	if deleted != 1 {
		_ = lockTx.Rollback()
		t.Fatalf("first cleanup deleted %d outcomes, want 1", deleted)
	}
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := actionOutcomeCount(t, d, firstPlayer); got != 1 {
		t.Fatalf("locked player's outcomes = %d, want 1", got)
	}
	if got := actionOutcomeCount(t, d, secondPlayer); got != 0 {
		t.Fatalf("unlocked player's outcomes = %d, want 0", got)
	}
	deleted, err = engine.CleanupExpired(context.Background(), 1)
	if err != nil || deleted != 1 {
		t.Fatalf("second cleanup deleted %d outcomes, error %v; want 1", deleted, err)
	}
	if got := actionOutcomeCount(t, d, firstPlayer); got != 0 {
		t.Fatalf("final outcome count = %d, want 0", got)
	}
	if _, err := engine.CleanupExpired(context.Background(), 0); err == nil {
		t.Fatal("cleanup accepted a zero batch size")
	}
}

func setupAccount(t *testing.T, balance int64) (*sql.DB, int64) {
	t.Helper()
	d := testdb.Open(t)
	var playerID int64
	username := fmt.Sprintf("action_%x", sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano()))))[:24]
	if err := d.QueryRowContext(context.Background(),
		`INSERT INTO accounts (username, password_hash, turns_balance, turns_updated_at)
		 VALUES ($1, 'test', $2, clock_timestamp()) RETURNING id`,
		username, balance).Scan(&playerID); err != nil {
		t.Fatal(err)
	}
	return d, playerID
}

func actionOutcomeCount(t *testing.T, d *sql.DB, playerID int64) int {
	t.Helper()
	var count int
	if err := d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM stillworld_action_outcomes WHERE account_id = $1`, playerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func createCounterTable(t *testing.T, d *sql.DB) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(),
		`CREATE TABLE game_counter (
		     account_id BIGINT PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
		     value BIGINT NOT NULL CHECK (value >= 0)
		 )`); err != nil {
		t.Fatal(err)
	}
}

func newTestEngine(t *testing.T, d *sql.DB, handler action.Action) *Engine {
	t.Helper()
	keys, err := actiontoken.NewKeyRing("test", map[string][]byte{"test": bytes.Repeat([]byte{0x5a}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(d, "counter_game", turns.Policy{Cap: 10, Interval: 24 * time.Hour}, keys,
		[]actionregistry.Registration{{Name: "counter.increment", Handler: handler}})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func issue(t *testing.T, engine *Engine, playerID int64, actionName string) RequestID {
	t.Helper()
	requestID, err := engine.IssueRequestID(context.Background(), playerID, actionName)
	if err != nil {
		t.Fatal(err)
	}
	return requestID
}

func incrementAction(applyCalls *atomic.Int32, failOnApply, rejectOnApply int32) action.Action {
	return actionFunc(func(_ context.Context, player action.Player, _ action.DB, input json.RawMessage) (action.Prepared, error) {
		var submitted struct {
			Amount int64 `json:"amount"`
		}
		decoder := json.NewDecoder(bytes.NewReader(input))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&submitted); err != nil {
			return action.Prepared{}, err
		}
		if submitted.Amount <= 0 {
			return action.Prepared{}, &action.Rejection{Code: "counter.invalid_amount", Message: "Amount must be positive."}
		}
		playerID := player.ID
		return action.Prepared{
			Cost: submitted.Amount,
			Apply: func(ctx context.Context, db action.DB) (json.RawMessage, error) {
				call := applyCalls.Add(1)
				var value int64
				err := db.QueryRowContext(ctx,
					`INSERT INTO game_counter (account_id, value) VALUES ($1, $2)
					 ON CONFLICT (account_id) DO UPDATE SET value = game_counter.value + EXCLUDED.value
					 RETURNING value`,
					playerID, submitted.Amount).Scan(&value)
				if err != nil {
					return nil, err
				}
				if call == failOnApply {
					return nil, errors.New("temporary failure")
				}
				if call == rejectOnApply {
					return nil, &action.Rejection{Code: "counter.denied", Message: "The counter change was rejected."}
				}
				result, err := json.Marshal(struct {
					Value int64 `json:"value"`
				}{Value: value})
				return result, err
			},
		}, nil
	})
}

func counterValue(t *testing.T, d *sql.DB, playerID int64) int64 {
	t.Helper()
	var value int64
	if err := d.QueryRowContext(context.Background(),
		`SELECT value FROM game_counter WHERE account_id = $1`, playerID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func counterRowCount(t *testing.T, d *sql.DB, playerID int64) int {
	t.Helper()
	var count int
	if err := d.QueryRowContext(context.Background(),
		`SELECT count(*) FROM game_counter WHERE account_id = $1`, playerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func turnsBalance(t *testing.T, d *sql.DB, playerID int64) int64 {
	t.Helper()
	var balance int64
	if err := d.QueryRowContext(context.Background(),
		`SELECT turns_balance::bigint FROM accounts WHERE id = $1`, playerID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	return balance
}
