package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/hivecommons/dibs/pkg/plaud"
	"github.com/hivecommons/dibs/pkg/wisprflow"
)

// ingestCommand is the `dibs ingest <source>` subcommand.
const ingestCommand = "ingest"

// ingestSources maps each `dibs ingest <source>` name to its command.
var ingestSources = map[string]func(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int{
	"plaud":     plaud.Main,
	"wisprflow": wisprflow.Main,
}

// ingest runs `dibs ingest <source> [flags]` and returns the exit code.
func ingest(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) > 0 {
		if run, ok := ingestSources[args[0]]; ok {
			return run(ctx, args[1:], os.Getenv, stderr)
		}
	}
	fmt.Fprintln(stderr, "usage: dibs ingest plaud|wisprflow [flags]")
	return 2
}
