// Command poppy provides fleet-wide git awareness across personal machines.
package main

import (
	"context"
	"os"

	"github.com/mads/poppy/internal/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:]))
}
