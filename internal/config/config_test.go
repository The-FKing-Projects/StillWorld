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
