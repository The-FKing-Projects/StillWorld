// Package config loads StillWorld runtime configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Config holds runtime settings.
type Config struct {
	// DatabaseURL is a PostgreSQL connection string.
	DatabaseURL string
	// ListenAddr is the address the HTTP server binds to.
	ListenAddr string
	// InsecureCookies drops the Secure cookie flag. Use only for local
	// development over plain HTTP.
	InsecureCookies bool
	// BehindProxy trusts the rightmost X-Forwarded-For entry as the client IP.
	// Enable only when a reverse proxy you control sets that header.
	BehindProxy bool
	// SharedRateLimits stores authentication limits in PostgreSQL.
	SharedRateLimits bool
}

// FromEnv reads configuration from environment variables.
func FromEnv() (Config, error) {
	return fromLookup(os.LookupEnv)
}

func fromLookup(lookup func(string) (string, bool)) (Config, error) {
	url, ok := lookup("STILLWORLD_DATABASE_URL")
	if !ok || url == "" {
		return Config{}, errors.New("STILLWORLD_DATABASE_URL is required")
	}
	cfg := Config{DatabaseURL: url, ListenAddr: "127.0.0.1:8080"}
	if v, ok := lookup("STILLWORLD_LISTEN_ADDR"); ok && v != "" {
		cfg.ListenAddr = v
	}
	var err error
	if cfg.InsecureCookies, err = boolVar(lookup, "STILLWORLD_INSECURE_COOKIES"); err != nil {
		return Config{}, err
	}
	if cfg.BehindProxy, err = boolVar(lookup, "STILLWORLD_BEHIND_PROXY"); err != nil {
		return Config{}, err
	}
	if cfg.SharedRateLimits, err = boolVar(lookup, "STILLWORLD_SHARED_RATE_LIMITS"); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func boolVar(lookup func(string) (string, bool), name string) (bool, error) {
	v, ok := lookup(name)
	if !ok || v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return b, nil
}
