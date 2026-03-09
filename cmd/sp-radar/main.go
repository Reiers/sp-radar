package main

import (
	"fmt"
	"os"

	"github.com/urfave/cli/v2"
)

var version = "dev"

func main() {
	app := &cli.App{
		Name:    "sp-radar",
		Usage:   "Filecoin Storage Provider network scanner",
		Version: version,
		Commands: []*cli.Command{
			scanCmd,
			serveCmd,
		},
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
