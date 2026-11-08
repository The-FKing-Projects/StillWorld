// Command stillworld is the entry point for the StillWorld server.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

const version = "0.0.0-dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stillworld", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, "stillworld", version)
		return 0
	}

	fmt.Fprintln(stderr, "stillworld: no server implemented yet")
	return 1
}
