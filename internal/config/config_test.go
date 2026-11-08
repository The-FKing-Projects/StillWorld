package config

import "testing"

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestFromLookup(t *testing.T) {
	cfg, err := fromLookup(lookupFrom(map[string]string{"STILLWORLD_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://x" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestFromLookupMissing(t *testing.T) {
	for _, m := range []map[string]string{{}, {"STILLWORLD_DATABASE_URL": ""}} {
		if _, err := fromLookup(lookupFrom(m)); err == nil {
			t.Fatalf("expected error for %v", m)
		}
	}
}

func TestDefaultsAndOverrides(t *testing.T) {
	cfg, err := fromLookup(lookupFrom(map[string]string{"STILLWORLD_DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" || cfg.InsecureCookies || cfg.BehindProxy {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	cfg, err = fromLookup(lookupFrom(map[string]string{
		"STILLWORLD_DATABASE_URL":     "postgres://x",
		"STILLWORLD_LISTEN_ADDR":      ":9000",
		"STILLWORLD_INSECURE_COOKIES": "true",
		"STILLWORLD_BEHIND_PROXY":     "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9000" || !cfg.InsecureCookies || !cfg.BehindProxy {
		t.Fatalf("unexpected overrides: %+v", cfg)
	}
}

func TestInvalidBool(t *testing.T) {
	_, err := fromLookup(lookupFrom(map[string]string{
		"STILLWORLD_DATABASE_URL":     "postgres://x",
		"STILLWORLD_INSECURE_COOKIES": "maybe",
	}))
	if err == nil {
		t.Fatal("expected error for invalid boolean")
	}
}
