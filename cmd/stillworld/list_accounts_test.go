package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/internal/testdb"
)

func TestListAccountsRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-after=-1"},
		{"-limit=0"},
		{"-limit=1001"},
		{"unexpected"},
	} {
		var out, errOut bytes.Buffer
		if code := listAccounts(args, &out, &errOut); code != 2 || errOut.Len() == 0 {
			t.Errorf("args=%v code=%d error=%q", args, code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"list-accounts", "-limit=0"}, &out, &errOut); code != 2 {
		t.Fatalf("command dispatch exit code=%d error=%q", code, errOut.String())
	}
}

func TestWriteAccountsPagination(t *testing.T) {
	database := testdb.Open(t)
	ctx := context.Background()
	names := []string{"alice", "bad\tname\x1b", "charlie"}
	ids := make([]int64, len(names))
	for i, name := range names {
		if err := database.QueryRowContext(ctx,
			`INSERT INTO accounts (username, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
			name, fmt.Sprintf("private%d@example.invalid", i), "secret-hash").Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	next, err := writeAccounts(ctx, database, 0, 2, &out)
	if err != nil || next != ids[1] {
		t.Fatalf("first page next=%d error=%v", next, err)
	}
	want := fmt.Sprintf("ID\tUSERNAME\n%d\t%q\n%d\t%q\n", ids[0], names[0], ids[1], names[1])
	if out.String() != want {
		t.Fatalf("first page = %q, want %q", out.String(), want)
	}
	if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "secret-hash") ||
		strings.ContainsRune(out.String(), '\x1b') {
		t.Fatal("account listing exposed private data or terminal control characters")
	}
	out.Reset()
	next, err = writeAccounts(ctx, database, next, 2, &out)
	if err != nil || next != 0 || out.String() != fmt.Sprintf("ID\tUSERNAME\n%d\t%q\n", ids[2], names[2]) {
		t.Fatalf("last page next=%d output=%q error=%v", next, out.String(), err)
	}
	out.Reset()
	next, err = writeAccounts(ctx, database, ids[2], 2, &out)
	if err != nil || next != 0 || out.String() != "ID\tUSERNAME\n" {
		t.Fatalf("empty page next=%d output=%q error=%v", next, out.String(), err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("output failed")
}

func TestWriteAccountsReportsOutputFailure(t *testing.T) {
	database := testdb.Open(t)
	if _, err := writeAccounts(context.Background(), database, 0, 1, failingWriter{}); err == nil ||
		!strings.Contains(err.Error(), "output failed") {
		t.Fatalf("output error = %v", err)
	}
}
