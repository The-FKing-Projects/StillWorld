// Package config loads StillWorld runtime configuration from the environment.
package config

import (
	"errors"
	"os"
)

// Config holds runtime settings.
type Config struct {
	// DatabaseURL is a PostgreSQL connection string.
	DatabaseURL string
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
	return Config{DatabaseURL: url}, nil
}
