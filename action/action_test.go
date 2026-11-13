package action

import (
	"errors"
	"fmt"
	"testing"
)

func TestRejectionCanBeWrapped(t *testing.T) {
	want := &Rejection{Code: "counter.invalid_amount", Message: "Amount must be positive."}
	err := fmt.Errorf("prepare action: %w", want)

	var got *Rejection
	if !errors.As(err, &got) {
		t.Fatal("wrapped rejection was not recognized")
	}
	if got != want {
		t.Fatal("errors.As returned a different rejection")
	}
	if got.Error() != want.Message {
		t.Fatalf("Error() = %q, want %q", got.Error(), want.Message)
	}
}
