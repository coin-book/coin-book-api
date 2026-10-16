package main

import (
	"fmt"
	"os"

	"coin-book-api/internal/app"
	"coin-book-api/internal/platform/clock"
	"coin-book-api/internal/platform/config"
)

func main() {
	cfg := &config.Config{}
	cfg.Server.DocsEnabled = true
	_, api := app.New(cfg, nil, clock.System{}, app.Build{})

	spec, err := api.OpenAPI().YAML()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate openapi:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(spec); err != nil {
		os.Exit(1)
	}
}
