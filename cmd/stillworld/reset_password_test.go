package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadConfirmedPassword(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inputs  []string
		readErr error
		wantErr string
	}{
		{name: "match", inputs: []string{"password123", "password123"}},
		{name: "mismatch", inputs: []string{"password123", "different"}, wantErr: "do not match"},
		{name: "read failure", readErr: errors.New("terminal failed"), wantErr: "terminal failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			password, err := readConfirmedPassword(func(_ string) ([]byte, error) {
				if tc.readErr != nil {
					return nil, tc.readErr
				}
				value := []byte(tc.inputs[i])
				i++
				return value, nil
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil || string(password) != tc.inputs[0] {
				t.Fatalf("password confirmation failed: %v", err)
			}
		})
	}
}

func TestResetPasswordRequiresUsernameAndTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := resetPassword(nil, nil, &out, &errOut); code != 2 {
		t.Fatalf("missing username: code=%d", code)
	}
	errOut.Reset()
	if code := resetPassword([]string{"player"}, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "interactive terminal") {
		t.Fatalf("nonterminal: code=%d error=%q", code, errOut.String())
	}
}
