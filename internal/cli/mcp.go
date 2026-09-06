package cli

import (
	"context"
	"flag"
	"os"

	"github.com/madelinecameron/poppy/internal/mcp"
)

func runMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, store, err := setupRead()
	if err != nil {
		return err
	}

	// The protocol owns stdout; diagnostics must not corrupt the JSON stream,
	// so nothing here writes there. Serve returns when stdin reaches EOF, which
	// is how an MCP client signals shutdown.
	return mcp.New(cfg, store).Serve(ctx, os.Stdin, os.Stdout)
}
