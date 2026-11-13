package actiondata

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/action"
)

func TestValidateInput(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "object", data: []byte(`{"amount":3}`)},
		{name: "nested objects and arrays", data: []byte(`{"items":[{"x":1},{"x":2}]}`)},
		{name: "duplicate top-level key", data: []byte(`{"amount":1,"amount":2}`), want: ErrInvalidInput},
		{name: "duplicate nested key", data: []byte(`{"item":{"x":1,"x":2}}`), want: ErrInvalidInput},
		{name: "escaped duplicate key", data: []byte(`{"a":1,"\u0061":2}`), want: ErrInvalidInput},
		{name: "non-object", data: []byte(`[1,2]`), want: ErrInvalidInput},
		{name: "trailing value", data: []byte(`{"x":1} {"y":2}`), want: ErrInvalidInput},
		{name: "trailing junk", data: []byte(`{"x":1}x`), want: ErrInvalidInput},
		{name: "empty", data: nil, want: ErrInvalidInput},
		{name: "invalid UTF-8", data: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, want: ErrInvalidInput},
		{name: "oversized", data: bytes.Repeat([]byte{' '}, MaxInputBytes+1), want: ErrInputTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInput(tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateInput() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidateResultAllowsAnySingleValue(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`{"value":3}`),
		[]byte(`[1,2]`),
		[]byte(`"done"`),
		[]byte(`null`),
	} {
		if err := ValidateResult(data); err != nil {
			t.Errorf("ValidateResult(%s) = %v", data, err)
		}
	}
	for _, data := range [][]byte{
		[]byte(`{"x":1,"x":2}`),
		[]byte(`{"x":{"y":1,"y":2}}`),
		[]byte(`true false`),
		nil,
	} {
		if err := ValidateResult(data); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("ValidateResult(%s) error = %v, want ErrInvalidResult", data, err)
		}
	}
	if err := ValidateResult(bytes.Repeat([]byte{' '}, MaxResultBytes+1)); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("oversized result error = %v, want ErrResultTooLarge", err)
	}
}

func TestFingerprintBindsExactBytesAndAction(t *testing.T) {
	base := Fingerprint("counter.increment", []byte(`{"amount":3}`))
	if base == Fingerprint("counter.reset", []byte(`{"amount":3}`)) {
		t.Fatal("fingerprint did not bind the action name")
	}
	if base == Fingerprint("counter.increment", []byte(`{ "amount":3}`)) {
		t.Fatal("fingerprint canonicalized whitespace")
	}
	if base == Fingerprint("counter.increment", []byte(`{"amount":3,"x":0}`)) {
		t.Fatal("fingerprint did not bind the complete payload")
	}
}

func TestValidateRejection(t *testing.T) {
	tests := []struct {
		name      string
		rejection *action.Rejection
		valid     bool
	}{
		{name: "valid", rejection: &action.Rejection{Code: "counter.invalid_amount", Message: "Amount must be positive."}, valid: true},
		{name: "nil"},
		{name: "unnamespaced code", rejection: &action.Rejection{Code: "invalid", Message: "Try again."}},
		{name: "empty code", rejection: &action.Rejection{Message: "Try again."}},
		{name: "invalid code character", rejection: &action.Rejection{Code: "counter.invalid/amount", Message: "Try again."}},
		{name: "empty message", rejection: &action.Rejection{Code: "counter.invalid", Message: ""}},
		{name: "oversized message", rejection: &action.Rejection{Code: "counter.invalid", Message: strings.Repeat("x", 1025)}},
		{name: "NUL message", rejection: &action.Rejection{Code: "counter.invalid", Message: "bad\x00message"}},
		{name: "invalid UTF-8 message", rejection: &action.Rejection{Code: "counter.invalid", Message: string([]byte{0xff})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRejection(tt.rejection)
			if tt.valid && err != nil {
				t.Fatalf("ValidateRejection() = %v", err)
			}
			if !tt.valid && !errors.Is(err, ErrInvalidRejection) {
				t.Fatalf("ValidateRejection() error = %v, want ErrInvalidRejection", err)
			}
		})
	}
}
