package plaud

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/hivecommons/dibs/pkg/ingest"
)

const (
	// DefaultServer is the public Dibs origin.
	DefaultServer = ingest.DefaultServer
	// DefaultTags are attached to every ingested idea.
	DefaultTags = "plaud,voice-note"
	// EnvToken holds the hub bearer token (preferred over --token, which is
	// visible in the process list).
	EnvToken = ingest.EnvToken
	// EnvServer overrides the default --server.
	EnvServer = ingest.EnvServer
	// EnvMCPCommand overrides the default --mcp-command.
	EnvMCPCommand = "DIBS_PLAUD_MCP_COMMAND"
	// EnvHome overrides the default --home.
	EnvHome = "DIBS_PLAUD_HOME"
)

// Main is `dibs ingest plaud`: it returns a process exit code (0 ok, 1 run
// failure, 2 usage error). getenv is injected for tests.
func Main(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	fs := flag.NewFlagSet("dibs ingest plaud", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: dibs ingest plaud [flags]\n\nPolls the Plaud MCP server for new recordings and submits each as a private draft Dibs idea.\nThe hub bearer token is read from $%s (or --token).\n\n", EnvToken)
		fs.PrintDefaults()
	}
	shared := ingest.AddFlags(fs, "plaud", "recordings", DefaultTags, getenv)
	command := fs.String("mcp-command", orDefault(getenv(EnvMCPCommand), DefaultCommand), "Plaud MCP server command (whitespace-separated); $"+EnvMCPCommand)
	home := fs.String("home", getenv(EnvHome), "$HOME for the Plaud MCP server, i.e. the parent of its ~/.plaud token store (default: inherit); $"+EnvHome)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "dibs ingest plaud: unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	in := &ingest.Ingester{
		Open: openSource(func(ctx context.Context) (Tools, error) {
			c, err := Dial(ctx, *command, *home, stderr)
			if err != nil {
				return nil, err
			}
			return c, nil
		}),
		Overlap: relistOverlap,
	}
	if code := shared.Configure(in, getenv, stderr); code != 0 {
		return code
	}
	if err := in.Run(ctx, shared.Interval()); err != nil {
		fmt.Fprintf(stderr, "dibs ingest plaud: %v\n", err)
		return 1
	}
	return 0
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
