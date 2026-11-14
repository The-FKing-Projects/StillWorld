package main

import (
	"bytes"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

func TestPasswordBenchmark(t *testing.T) {
	var calls atomic.Int32
	var out, errOut bytes.Buffer
	hash := func(_ string, params auth.Params) (string, error) {
		if params != (auth.Params{MemoryKiB: 32, Iterations: 2, Parallelism: 1}) {
			t.Errorf("unexpected parameters: %+v", params)
		}
		calls.Add(1)
		return "", nil
	}
	code := benchmarkPassword([]string{"-memory-kib=32", "-iterations=2", "-parallelism=1", "-concurrency=2", "-samples=3"}, &out, &errOut, hash)
	if code != 0 || calls.Load() != 6 || !strings.Contains(out.String(), "64 KiB concurrently") ||
		!strings.Contains(out.String(), "(6 hashes)") {
		t.Fatalf("code=%d calls=%d output=%q error=%q", code, calls.Load(), out.String(), errOut.String())
	}
}

func TestPasswordBenchmarkRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-memory-kib=0"},
		{"-iterations=4294967296"},
		{"-parallelism=256"},
		{"-concurrency=5"},
		{"-samples=0"},
		{"unexpected"},
	} {
		var out, errOut bytes.Buffer
		code := benchmarkPassword(args, &out, &errOut, func(string, auth.Params) (string, error) {
			t.Error("hash called for invalid arguments")
			return "", nil
		})
		if code != 2 || errOut.Len() == 0 {
			t.Errorf("args=%v code=%d error=%q", args, code, errOut.String())
		}
	}
}

func TestPasswordBenchmarkReportsHashFailure(t *testing.T) {
	var out, errOut bytes.Buffer
	code := benchmarkPassword([]string{"-concurrency=2", "-samples=1"}, &out, &errOut, func(string, auth.Params) (string, error) {
		return "", errors.New("hash failed")
	})
	if code != 1 || !strings.Contains(errOut.String(), "hash failed") {
		t.Fatalf("code=%d error=%q", code, errOut.String())
	}
}

func TestRunPasswordBenchmarkRejectsInvalidArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"benchmark-password", "-samples=0"}, &out, &errOut); code != 2 {
		t.Fatalf("exit code=%d error=%q", code, errOut.String())
	}
}
