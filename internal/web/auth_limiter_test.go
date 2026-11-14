package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
	"github.com/The-FKing-Projects/StillWorld/recovery"
)

func TestSharedLimiterAcrossInstancesAndConcurrentReservations(t *testing.T) {
	database := testdb.Open(t)
	ctx := context.Background()
	first := newAuthLimiter(database, 1, 2, time.Hour, time.Now)
	second := newAuthLimiter(database, 1, 2, time.Hour, time.Now)
	const attempts = 12
	results := make(chan *authReservation, attempts)
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			limiter := first
			if i%2 == 1 {
				limiter = second
			}
			slot, err := limiter.reserve(ctx, "sensitive-key")
			results <- slot
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var accepted []*authReservation
	for slot := range results {
		if slot != nil {
			accepted = append(accepted, slot)
		}
	}
	if len(accepted) != 2 {
		t.Fatalf("admitted %d requests, want 2", len(accepted))
	}
	if err := first.finish(accepted[0], true, false); err != nil {
		t.Fatal(err)
	}
	if err := second.finish(accepted[1], false, false); err != nil {
		t.Fatal(err)
	}
	restarted := newAuthLimiter(database, 1, 2, time.Hour, time.Now)
	slot, err := restarted.reserve(ctx, "sensitive-key")
	if err != nil || slot == nil {
		t.Fatalf("remaining slot: %v", err)
	}
	if err := restarted.finish(slot, true, false); err != nil {
		t.Fatal(err)
	}
	if slot, err := restarted.reserve(ctx, "sensitive-key"); err != nil || slot != nil {
		t.Fatalf("limit lost across restart: slot=%v err=%v", slot, err)
	}
	var stored []byte
	var count int
	if err := database.QueryRowContext(ctx, `SELECT key_hash FROM stillworld_rate_limits WHERE scope = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == "sensitive-key" || len(stored) != 32 {
		t.Fatal("rate limiter stored a raw key")
	}
	if err := database.QueryRowContext(ctx, `SELECT entry_count FROM stillworld_rate_limit_capacity`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stored capacity = %d: %v", count, err)
	}
}

func TestSharedLimiterExpiresAbandonedReservations(t *testing.T) {
	database := testdb.Open(t)
	ctx := context.Background()
	limiter := newAuthLimiter(database, 1, 1, time.Hour, time.Now)
	old, err := limiter.reserve(ctx, "key")
	if err != nil || old == nil {
		t.Fatalf("old reservation: %v", err)
	}
	if slot, err := limiter.reserve(ctx, "key"); err != nil || slot != nil {
		t.Fatalf("pending reservation did not count: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE stillworld_rate_limits
		SET expires_at = clock_timestamp() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	current, err := limiter.reserve(ctx, "key")
	if err != nil || current == nil || current.generation == old.generation {
		t.Fatalf("new generation: %v", err)
	}
	if err := limiter.finish(old, true, false); err != nil {
		t.Fatal(err)
	}
	if slot, err := limiter.reserve(ctx, "key"); err != nil || slot != nil {
		t.Fatalf("old completion changed new window: %v", err)
	}
	if err := limiter.finish(current, true, false); err != nil {
		t.Fatal(err)
	}
	if slot, err := limiter.reserve(ctx, "key"); err != nil || slot != nil {
		t.Fatalf("new window quota lost: %v", err)
	}
}

func TestSharedLimiterFailsClosedAtCapacity(t *testing.T) {
	database := testdb.Open(t)
	if _, err := database.Exec(`UPDATE stillworld_rate_limit_capacity SET entry_count = $1`, sharedLimitMaxKeys); err != nil {
		t.Fatal(err)
	}
	limiter := newAuthLimiter(database, 1, 2, time.Hour, time.Now)
	if _, err := limiter.reserve(context.Background(), "key"); !errors.Is(err, errLimitCapacity) {
		t.Fatalf("capacity error: %v", err)
	}
}

func TestSharedLimiterRenewsExpiredEntryOutsidePurgeBatch(t *testing.T) {
	database := testdb.Open(t)
	ctx := context.Background()
	limiter := newAuthLimiter(database, 1, 1, time.Hour, time.Now)
	old, err := limiter.reserve(ctx, "target")
	if err != nil || old == nil {
		t.Fatalf("old reservation: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE stillworld_rate_limits
		SET expires_at = clock_timestamp() - interval '1 minute';
		INSERT INTO stillworld_rate_limits (scope, key_hash, failures, pending, expires_at)
		SELECT 1, decode(lpad(to_hex(n), 64, '0'), 'hex'), 0, 0,
			clock_timestamp() - interval '1 hour' FROM generate_series(1, 70) AS n;
		UPDATE stillworld_rate_limit_capacity SET entry_count = entry_count + 70`); err != nil {
		t.Fatal(err)
	}
	current, err := limiter.reserve(ctx, "target")
	if err != nil || current == nil || current.generation == old.generation {
		t.Fatalf("renewed reservation: %v", err)
	}
	if err := limiter.finish(old, true, false); err != nil {
		t.Fatal(err)
	}
	if slot, err := limiter.reserve(ctx, "target"); err != nil || slot != nil {
		t.Fatalf("old completion changed renewed reservation: %v", err)
	}
	if err := limiter.finish(current, true, false); err != nil {
		t.Fatal(err)
	}
	var actual, tracked int
	if err := database.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM stillworld_rate_limits), entry_count
		FROM stillworld_rate_limit_capacity`).Scan(&actual, &tracked); err != nil || actual != tracked {
		t.Fatalf("capacity = %d, rows = %d: %v", tracked, actual, err)
	}
}

func TestSharedLimiterSuccessfulLoginClearsFailures(t *testing.T) {
	database := testdb.Open(t)
	ctx := context.Background()
	limiter := newAuthLimiter(database, 2, 2, time.Hour, time.Now)
	first, err := limiter.reserve(ctx, "player")
	if err != nil || first == nil {
		t.Fatalf("first reservation: %v", err)
	}
	if err := limiter.finish(first, true, false); err != nil {
		t.Fatal(err)
	}
	second, err := limiter.reserve(ctx, "player")
	if err != nil || second == nil {
		t.Fatalf("second reservation: %v", err)
	}
	if err := limiter.finish(second, false, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		slot, err := limiter.reserve(ctx, "player")
		if err != nil || slot == nil {
			t.Fatalf("reservation after success %d: %v", i, err)
		}
		if err := limiter.finish(slot, true, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSharedLoginLimitsPersistAcrossServers(t *testing.T) {
	database := testdb.Open(t)
	authenticator := newFakeAuth()
	authenticator.users["player"] = "password123"
	first := New(authenticator, Options{SharedRateLimits: database}).Handler()
	second := New(authenticator, Options{SharedRateLimits: database}).Handler()
	for i := 0; i < maxLoginFailsID; i++ {
		handler := first
		if i%2 == 1 {
			handler = second
		}
		response := post(handler, "/login", url.Values{"username": {"PLAYER"}, "password": {"wrong"}}, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, response.Code)
		}
	}
	restarted := New(authenticator, Options{SharedRateLimits: database}).Handler()
	if response := post(restarted, "/login", url.Values{"username": {"player"}, "password": {"password123"}}, nil); response.Code != http.StatusTooManyRequests {
		t.Fatalf("restarted server bypassed limit: %d", response.Code)
	}
}

func TestSharedSignupAndRecoveryLimits(t *testing.T) {
	database := testdb.Open(t)
	authenticator := recoveryStub{fakeAuth: newFakeAuth(), calls: make(chan string, 2)}
	mails := make(chan resetMail, 1)
	options := Options{SharedRateLimits: database, Recovery: &recovery.Config{
		PublicOrigin: "https://game.example", Sender: testSender{mails},
	}}
	first := New(authenticator, options).Handler()
	second := New(authenticator, options).Handler()
	for i := 0; i < maxSignupsIP; i++ {
		handler := first
		if i%2 == 1 {
			handler = second
		}
		response := post(handler, "/signup", url.Values{"username": {"player"}, "password": {"password123"}}, nil)
		if response.Code != http.StatusCreated && response.Code != http.StatusConflict {
			t.Fatalf("signup %d = %d", i, response.Code)
		}
	}
	if response := post(second, "/signup", url.Values{"username": {"player"}, "password": {"password123"}}, nil); response.Code != http.StatusTooManyRequests {
		t.Fatalf("shared signup quota = %d", response.Code)
	}
	form := url.Values{"email": {"known@example.com"}}
	original := post(first, "/forgot-password", form, nil)
	select {
	case <-authenticator.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("first recovery request was not delivered")
	}
	select {
	case <-mails:
	case <-time.After(5 * time.Second):
		t.Fatal("first recovery email was not sent")
	}
	form.Set("email", "KNOWN@example.com")
	repeated := post(second, "/forgot-password", form, nil)
	if original.Code != http.StatusOK || repeated.Code != original.Code || repeated.Body.String() != original.Body.String() {
		t.Fatal("shared recovery quota changed the generic response")
	}
	select {
	case <-authenticator.calls:
		t.Fatal("shared recovery quota did not suppress a repeated request")
	default:
	}
}

func TestSharedLimitsFailClosedOnDatabaseFailure(t *testing.T) {
	database := testdb.Open(t)
	if _, err := database.Exec(`DROP TABLE stillworld_rate_limits`); err != nil {
		t.Fatal(err)
	}

	authenticator := newFakeAuth()
	mails := make(chan resetMail, 1)
	handler := New(authenticator, Options{SharedRateLimits: database, Recovery: &recovery.Config{
		PublicOrigin: "https://game.example", Sender: testSender{mails},
	}}).Handler()
	for _, path := range []string{"/signup", "/login", "/forgot-password", "/reset-password"} {
		response := post(handler, path, url.Values{"username": {"player"}, "password": {"password123"}, "email": {"a@example.invalid"}}, nil)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d", path, response.Code)
		}
	}
	if authenticator.logins != 0 || len(authenticator.users) != 0 {
		t.Fatal("authentication ran without shared admission")
	}
	select {
	case <-mails:
		t.Fatal("sent mail without shared admission")
	default:
	}
}

type failAfterLoginAuth struct {
	*fakeAuth
	drop func() error
}

func (a failAfterLoginAuth) Login(_ context.Context, _, _ string) (string, error) {
	if err := a.drop(); err != nil {
		return "", err
	}
	return "session-token", nil
}

func TestSharedLoginDoesNotReturnSessionWhenFinalizationFails(t *testing.T) {
	database := testdb.Open(t)
	authenticator := failAfterLoginAuth{fakeAuth: newFakeAuth(), drop: func() error {
		_, err := database.Exec(`DROP TABLE stillworld_rate_limits`)
		return err
	}}
	handler := New(authenticator, Options{SharedRateLimits: database}).Handler()
	response := post(handler, "/login", url.Values{"username": {"player"}, "password": {"password123"}}, nil)
	if response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
		t.Fatalf("finalization failure returned session: %d", response.Code)
	}
}
