package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/internal/config"
	"github.com/The-FKing-Projects/StillWorld/internal/db"
	"golang.org/x/term"
)

func resetPassword(args []string, terminal *os.File, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: stillworld reset-password <username>")
		return 2
	}
	if terminal == nil || !term.IsTerminal(int(terminal.Fd())) {
		fmt.Fprintln(stderr, "stillworld: password reset requires an interactive terminal")
		return 1
	}
	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	database, err := db.Open(connectCtx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	defer database.Close()
	svc, err := auth.NewService(database)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	password, err := readTerminalPassword(terminal, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "stillworld:", err)
		return 1
	}
	defer clear(password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.ResetPassword(ctx, args[0], string(password)); err != nil {
		fmt.Fprintln(stderr, "stillworld: reset password:", err)
		return 1
	}
	fmt.Fprintln(stdout, "Password reset; existing sessions revoked.")
	return 0
}

type terminalIO struct {
	io.Reader
	io.Writer
}

func readTerminalPassword(terminal *os.File, prompt io.Writer) (password []byte, err error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fd := int(terminal.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("prepare terminal: %w", err)
	}
	defer func() {
		if restoreErr := term.Restore(fd, state); restoreErr != nil {
			clear(password)
			password = nil
			err = errors.Join(err, fmt.Errorf("restore terminal: %w", restoreErr))
		}
	}()
	editor := term.NewTerminal(terminalIO{Reader: terminal, Writer: prompt}, "")
	type result struct {
		password string
		err      error
	}
	read := func(label string) ([]byte, error) {
		results := make(chan result, 1)
		go func() {
			value, err := editor.ReadPassword(label)
			results <- result{password: value, err: err}
		}()
		select {
		case <-ctx.Done():
			return nil, errors.New("password entry interrupted")
		case r := <-results:
			if ctx.Err() != nil {
				return nil, errors.New("password entry interrupted")
			}
			return []byte(r.password), r.err
		}
	}
	return readConfirmedPassword(read)
}

func readConfirmedPassword(read func(string) ([]byte, error)) ([]byte, error) {
	password, err := read("New password: ")
	if err != nil {
		clear(password)
		return nil, fmt.Errorf("read password: %w", err)
	}
	confirmation, err := read("Confirm password: ")
	defer clear(confirmation)
	if err != nil {
		clear(password)
		return nil, fmt.Errorf("read password confirmation: %w", err)
	}
	if !bytes.Equal(password, confirmation) {
		clear(password)
		return nil, errors.New("passwords do not match")
	}
	return password, nil
}
