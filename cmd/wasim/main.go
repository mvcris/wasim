package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mvcris/wasim/internal/app"
	"github.com/mvcris/wasim/internal/config"
)

// version is set at build time with -ldflags.
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "wasim:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, err := config.Load(args, os.Getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, version, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			logger.Error("close database", "error", closeErr)
		}
	}()
	return application.Run(ctx)
}
