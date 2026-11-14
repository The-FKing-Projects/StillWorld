package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

func benchmarkPassword(args []string, stdout, stderr io.Writer, hash func(string, auth.Params) (string, error)) int {
	flags := flag.NewFlagSet("benchmark-password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	memory := flags.Uint("memory-kib", uint(auth.DefaultParams.MemoryKiB), "memory per hash in KiB")
	iterations := flags.Uint("iterations", uint(auth.DefaultParams.Iterations), "Argon2id iterations")
	parallelism := flags.Uint("parallelism", uint(auth.DefaultParams.Parallelism), "Argon2id lanes per hash")
	concurrency := flags.Int("concurrency", 1, "simultaneous hashes (1-4)")
	samples := flags.Int("samples", 3, "number of batches (1-10)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *memory > math.MaxUint32 || *iterations > math.MaxUint32 ||
		*parallelism > math.MaxUint8 || *concurrency < 1 || *concurrency > 4 || *samples < 1 || *samples > 10 {
		fmt.Fprintln(stderr, "stillworld: invalid benchmark arguments")
		return 2
	}
	params := auth.Params{MemoryKiB: uint32(*memory), Iterations: uint32(*iterations), Parallelism: uint8(*parallelism)}
	if !auth.ValidParams(params) {
		fmt.Fprintln(stderr, "stillworld: invalid Argon2id parameters")
		return 2
	}
	fmt.Fprintf(stdout, "Argon2id: memory=%d KiB iterations=%d parallelism=%d concurrency=%d batches=%d\n",
		params.MemoryKiB, params.Iterations, params.Parallelism, *concurrency, *samples)
	fmt.Fprintf(stdout, "Hashing may use about %d KiB concurrently, excluding application and system overhead.\n", uint64(params.MemoryKiB)*uint64(*concurrency))

	type result struct {
		duration time.Duration
		err      error
	}
	var total, min, max time.Duration
	for batch := 0; batch < *samples; batch++ {
		start := make(chan struct{})
		results := make(chan result, *concurrency)
		for i := 0; i < *concurrency; i++ {
			go func() {
				<-start
				before := time.Now()
				_, err := hash("StillWorld benchmark password", params)
				results <- result{duration: time.Since(before), err: err}
			}()
		}
		close(start)
		var batchErr error
		for i := 0; i < *concurrency; i++ {
			r := <-results
			if r.err != nil {
				batchErr = r.err
			}
			total += r.duration
			if min == 0 || r.duration < min {
				min = r.duration
			}
			if r.duration > max {
				max = r.duration
			}
		}
		if batchErr != nil {
			fmt.Fprintln(stderr, "stillworld: password benchmark:", batchErr)
			return 1
		}
	}
	count := *samples * *concurrency
	fmt.Fprintf(stdout, "Per-hash duration: mean=%s min=%s max=%s (%d hashes)\n",
		(total / time.Duration(count)).Round(time.Microsecond),
		min.Round(time.Microsecond), max.Round(time.Microsecond), count)
	return 0
}
