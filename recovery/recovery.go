// Package recovery configures optional password-reset email delivery.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Sender delivers codes and links that do not contain the codes.
type Sender interface {
	SendReset(context.Context, string, string, string) error
	SendVerification(context.Context, string, string, string) error
}

// Config enables self-service password recovery.
type Config struct {
	PublicOrigin string
	Sender       Sender
}

// Validate checks the configured public origin and delivery implementation.
func (c Config) Validate() error {
	if c.Sender == nil {
		return errors.New("recovery: sender is required")
	}
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("recovery: public origin must be an origin without a path, query, or credentials")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("recovery: public origin must use HTTPS except on loopback")
		}
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return errors.New("recovery: invalid public origin port")
		}
	}
	return nil
}

// ValidateForCookies also checks that the public origin uses the scheme
// expected by the HTTP server's same-origin protection.
func (c Config) ValidateForCookies(insecureCookies bool) error {
	if err := c.Validate(); err != nil {
		return err
	}
	u, _ := url.Parse(c.PublicOrigin)
	expected := "https"
	if insecureCookies {
		expected = "http"
	}
	if u.Scheme != expected {
		return errors.New("recovery: public origin scheme must match the server cookie and origin settings")
	}
	return nil
}

// ResetURL returns the configured reset page URL without a token.
func (c Config) ResetURL() string {
	return strings.TrimSuffix(c.PublicOrigin, "/") + "/reset-password"
}

// VerificationURL returns the configured verification page URL without a token.
func (c Config) VerificationURL() string {
	return strings.TrimSuffix(c.PublicOrigin, "/") + "/verify-email"
}

// FromEnv configures SMTP recovery, or returns nil when no recovery settings
// are present. All SMTP connections require TLS.
func FromEnv() (*Config, error) {
	return fromLookup(os.LookupEnv)
}

func fromLookup(lookup func(string) (string, bool)) (*Config, error) {
	keys := []string{"STILLWORLD_RECOVERY_ORIGIN", "STILLWORLD_SMTP_ADDRESS", "STILLWORLD_SMTP_FROM",
		"STILLWORLD_SMTP_MODE", "STILLWORLD_SMTP_USERNAME", "STILLWORLD_SMTP_PASSWORD"}
	values := make(map[string]string, len(keys))
	configured := false
	for _, key := range keys {
		value, ok := lookup(key)
		if ok && value != "" {
			configured = true
			values[key] = value
		}
	}
	if !configured {
		return nil, nil
	}
	address, from := values["STILLWORLD_SMTP_ADDRESS"], values["STILLWORLD_SMTP_FROM"]
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return nil, errors.New("recovery: SMTP address must include host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("recovery: invalid SMTP port")
	}
	if !validAddress(from) {
		return nil, errors.New("recovery: SMTP sender must be a plain email address")
	}
	mode := values["STILLWORLD_SMTP_MODE"]
	if mode == "" {
		mode = "starttls"
	}
	if mode != "starttls" && mode != "tls" {
		return nil, errors.New("recovery: SMTP mode must be starttls or tls")
	}
	username, password := values["STILLWORLD_SMTP_USERNAME"], values["STILLWORLD_SMTP_PASSWORD"]
	if (username == "") != (password == "") || strings.ContainsAny(username, "\r\n\x00") || strings.ContainsAny(password, "\r\n\x00") {
		return nil, errors.New("recovery: SMTP username and password must both be provided without control characters")
	}
	cfg := &Config{
		PublicOrigin: values["STILLWORLD_RECOVERY_ORIGIN"],
		Sender:       &smtpSender{address: address, host: host, from: from, mode: mode, username: username, password: password},
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("recovery configuration: %w", err)
	}
	return cfg, nil
}

func validAddress(value string) bool {
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	addr, err := mail.ParseAddress(value)
	return err == nil && addr.Name == "" && addr.Address == value
}
