package turns

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
)

func TestPolicy(t *testing.T) {
	for _, p := range []Policy{
		{0, time.Second}, {-1, time.Second}, {1, 0},
		{1, -time.Second}, {1, time.Nanosecond}, {1, time.Microsecond + time.Nanosecond},
	} {
		if _, err := New(p, nil); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("New(%+v): %v", p, err)
		}
	}
	if _, err := New(Policy{math.MaxInt64, time.Microsecond}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCalculate(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := New(Policy{10, time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		balance   string
		elapsed   time.Duration
		available int64
		updated   time.Duration
	}{
		{"no time", "2", 0, 2, 0},
		{"sub Turn", "0", 900 * time.Millisecond, 0, 0},
		{"fractional elapsed", "2", 1500 * time.Millisecond, 3, time.Second},
		{"stored fraction", "2.75", 1500 * time.Millisecond, 3, time.Second},
		{"exact cap", "9", time.Second, 10, time.Second},
		{"cap drops remainder", "9", 1500 * time.Millisecond, 10, 1500 * time.Millisecond},
		{"long idle", "0", 24 * time.Hour, 10, 24 * time.Hour},
		{"above new cap", "20.5", 0, 10, 0},
		{"huge numeric", "999999999999999999999999999999999999", 0, 10, 0},
		{"backward clock", "2.5", -time.Second, 2, 0},
		{"backward at cap", "20", -time.Second, 10, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := s.calculate(tc.balance, base, base.Add(tc.elapsed))
			if err != nil {
				t.Fatal(err)
			}
			if state.available != tc.available || !state.updated.Equal(base.Add(tc.updated)) {
				t.Fatalf("got %+v, want available %d, timestamp %s", state, tc.available, base.Add(tc.updated))
			}
		})
	}
	for _, value := range []string{"-1", "NaN", "Infinity", "-Infinity"} {
		if _, err := s.calculate(value, base, base); !errors.Is(err, ErrInvalidState) {
			t.Errorf("balance %q: %v", value, err)
		}
	}
}

func TestArithmeticExtremes(t *testing.T) {
	start := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	s, _ := New(Policy{math.MaxInt64, time.Microsecond}, nil)
	state, err := s.calculate("0", start, end)
	want := (end.Unix() - start.Unix()) * 1_000_000
	if err != nil || state.available != want || !state.updated.Equal(end) {
		t.Fatalf("large elapsed: %+v, %v; want %d", state, err, want)
	}
	state, err = s.calculate("9223372036854775806.99", start, start.Add(time.Microsecond))
	if err != nil || state.available != math.MaxInt64 {
		t.Fatalf("maximum cap: %+v, %v", state, err)
	}
}

type fakeClock struct{ micros atomic.Int64 }

func (c *fakeClock) now() time.Time {
	return time.UnixMicro(c.micros.Load()).UTC()
}

func (c *fakeClock) set(t time.Time) {
	c.micros.Store(t.UnixMicro())
}

func setup(t *testing.T, balance string) (*sql.DB, *Service, *fakeClock, int64) {
	t.Helper()
	d := testdb.Open(t)
	clock := &fakeClock{}
	clock.set(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s, err := New(Policy{10, time.Second}, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = d.QueryRow(`INSERT INTO accounts (username, password_hash, turns_balance, turns_updated_at)
		VALUES ('player', 'unused', $1::numeric, $2) RETURNING id`, balance, clock.now()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return d, s, clock, id
}

func begin(t *testing.T, d *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func commit(t *testing.T, tx *sql.Tx) {
	t.Helper()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestFractionalSpends(t *testing.T) {
	d, s, clock, id := setup(t, "2.75")
	base := clock.now()
	for _, tc := range []struct {
		elapsed time.Duration
		cost    int64
		want    int64
	}{
		{750 * time.Millisecond, 1, 1},
		{1500 * time.Millisecond, 1, 1},
		{1999 * time.Millisecond, 0, 1},
		{2 * time.Second, 0, 2},
	} {
		clock.set(base.Add(tc.elapsed))
		tx := begin(t, d)
		got, err := s.Spend(context.Background(), tx, id, tc.cost)
		if err != nil || got != tc.want {
			t.Fatalf("at %s: got %d, %v; want %d", tc.elapsed, got, err, tc.want)
		}
		commit(t, tx)
	}
	var balance string
	if err := d.QueryRow(`SELECT turns_balance::text FROM accounts WHERE id = $1`, id).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != "2.75" {
		t.Fatalf("stored fractional balance = %s", balance)
	}
}

func TestCapAndBackwardClock(t *testing.T) {
	d, s, clock, id := setup(t, "9")
	base := clock.now()
	clock.set(base.Add(1500 * time.Millisecond))
	tx := begin(t, d)
	if got, err := s.Spend(context.Background(), tx, id, 10); err != nil || got != 0 {
		t.Fatalf("spend at cap: %d, %v", got, err)
	}
	commit(t, tx)
	for _, tc := range []struct {
		at   time.Duration
		want int64
	}{
		{time.Second, 0},
		{2 * time.Second, 0},
		{2500 * time.Millisecond, 1},
	} {
		clock.set(base.Add(tc.at))
		tx = begin(t, d)
		if got, err := s.Current(context.Background(), tx, id); err != nil || got != tc.want {
			t.Fatalf("current at %s: %d, %v", tc.at, got, err)
		}
		commit(t, tx)
	}
}

func TestErrorsAndRollback(t *testing.T) {
	d, s, clock, id := setup(t, "1")
	base := clock.now()
	clock.set(base.Add(1500 * time.Millisecond))
	for _, tc := range []struct {
		cost int64
		err  error
	}{{-1, ErrInvalidCost}, {3, ErrInsufficient}, {math.MaxInt64, ErrInsufficient}} {
		tx := begin(t, d)
		if _, err := s.Spend(context.Background(), tx, id, tc.cost); !errors.Is(err, tc.err) {
			t.Fatalf("cost %d: %v", tc.cost, err)
		}
		commit(t, tx)
	}
	var balance string
	var updated time.Time
	if err := d.QueryRow(`SELECT turns_balance::text, turns_updated_at FROM accounts WHERE id = $1`, id).
		Scan(&balance, &updated); err != nil {
		t.Fatal(err)
	}
	if balance != "1" || !updated.Equal(base) {
		t.Fatalf("failed spend changed state: %s, %s", balance, updated)
	}
	tx := begin(t, d)
	if _, err := s.Spend(context.Background(), tx, id, 2); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx = begin(t, d)
	if got, err := s.Current(context.Background(), tx, id); err != nil || got != 2 {
		t.Fatalf("rollback: %d, %v", got, err)
	}
	commit(t, tx)
	tx = begin(t, d)
	if _, err := s.Current(context.Background(), tx, id+1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing account: %v", err)
	}
}

func TestConcurrentSpends(t *testing.T) {
	d, s, _, id := setup(t, "5")
	var success, insufficient atomic.Int64
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			tx, err := d.BeginTx(context.Background(), nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer tx.Rollback()
			_, err = s.Spend(context.Background(), tx, id, 1)
			if errors.Is(err, ErrInsufficient) {
				insufficient.Add(1)
			} else if err != nil {
				t.Error(err)
			} else if err := tx.Commit(); err != nil {
				t.Error(err)
			} else {
				success.Add(1)
			}
		})
	}
	wg.Wait()
	if success.Load() != 5 || insufficient.Load() != 7 {
		t.Fatalf("success=%d, insufficient=%d", success.Load(), insufficient.Load())
	}
	tx := begin(t, d)
	if got, err := s.Current(context.Background(), tx, id); err != nil || got != 0 {
		t.Fatalf("final balance: %d, %v", got, err)
	}
}

func TestClockSampledAfterLock(t *testing.T) {
	d, s, clock, id := setup(t, "0")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder := begin(t, d)
	if _, err := s.Current(ctx, holder, id); err != nil {
		t.Fatal(err)
	}
	waiter := begin(t, d)
	var pid int
	if err := waiter.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	sampled := make(chan struct{}, 1)
	s.now = func() time.Time {
		sampled <- struct{}{}
		return clock.now()
	}
	type result struct {
		balance int64
		err     error
	}
	done := make(chan result, 1)
	go func() {
		balance, err := s.Spend(ctx, waiter, id, 1)
		done <- result{balance, err}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := d.QueryRowContext(ctx,
			`SELECT COALESCE(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		} else if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	select {
	case <-sampled:
		t.Fatal("clock sampled before row lock acquired")
	default:
	}
	clock.set(clock.now().Add(time.Second))
	commit(t, holder)
	select {
	case got := <-done:
		if got.err != nil || got.balance != 0 {
			t.Fatalf("spend after waiting: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	commit(t, waiter)
}
