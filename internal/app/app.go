// Package app wires the simulator's parts into one runnable process.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mvcris/wasim/internal/config"
	"github.com/mvcris/wasim/internal/console"
	"github.com/mvcris/wasim/internal/events"
	"github.com/mvcris/wasim/internal/graphapi"
	"github.com/mvcris/wasim/internal/sim"
	"github.com/mvcris/wasim/internal/store"
	"github.com/mvcris/wasim/internal/webhook"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 5 * time.Second
	// A consumer usually takes longer to boot than wasim does in a compose file.
	handshakeAttempts = 15
	handshakeInterval = 2 * time.Second
)

// App is an assembled simulator.
type App struct {
	Service        *sim.Service
	GraphHandler   http.Handler
	ConsoleHandler http.Handler

	config     config.Config
	store      *store.Store
	dispatcher *webhook.Dispatcher
	logger     *slog.Logger
}

// New builds the application and prepares its database.
func New(ctx context.Context, cfg config.Config, version string, logger *slog.Logger) (*App, error) {
	seed, err := sim.LoadSeed(cfg.SeedFile)
	if err != nil {
		return nil, err
	}
	database, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	if err := database.ReleaseInFlight(ctx, time.Now()); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("release in-flight webhooks: %w", err)
	}

	broker := events.NewBroker()
	dispatcher := webhook.NewDispatcher(database, webhook.Config{
		URL:         cfg.WebhookURL,
		AppSecret:   cfg.AppSecret,
		Concurrency: cfg.WebhookWorkers,
		MaxAttempts: cfg.WebhookAttempts,
		Timeout:     cfg.WebhookTimeout,
		RetryBase:   cfg.WebhookRetryBase,
		RetryMax:    cfg.WebhookRetryLimit,
	}, logger, broker.Publish)

	service := sim.New(database, sim.Config{
		Account: webhook.Account{
			BusinessAccountID:  cfg.BusinessAccountID,
			PhoneNumberID:      cfg.PhoneNumberID,
			DisplayPhoneNumber: cfg.DisplayPhoneNumber,
		},
		PublicURL:          cfg.PublicURL,
		MediaDir:           cfg.MediaDir,
		WebhookURL:         cfg.WebhookURL,
		VerifyToken:        cfg.VerifyToken,
		AutoStatuses:       cfg.AutoStatuses,
		StatusDelay:        cfg.StatusDelay,
		EnforceWindow:      cfg.EnforceWindow,
		WindowErrorChannel: cfg.WindowErrorChannel,
		EscapeUnicode:      cfg.EscapeUnicode,
		Seed:               seed,
	}, sim.Hooks{OnChange: broker.Publish, OnEnqueue: dispatcher.Wake}, logger)
	if err := service.ApplySeed(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}

	consoleHandler, err := console.New(service, broker, version, logger)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	return &App{
		Service:        service,
		GraphHandler:   graphapi.New(service, cfg.AccessTokens, logger),
		ConsoleHandler: consoleHandler,
		config:         cfg,
		store:          database,
		dispatcher:     dispatcher,
		logger:         logger,
	}, nil
}

// Close releases the database.
func (a *App) Close() error {
	return a.store.Close()
}

// RunDispatcher delivers webhooks until ctx is canceled.
func (a *App) RunDispatcher(ctx context.Context) {
	a.dispatcher.Run(ctx)
}

// Run serves both HTTP servers and delivers webhooks until ctx is canceled.
func (a *App) Run(ctx context.Context) error {
	if a.config.WebhookURL != "" && a.config.AppSecret == "" {
		a.logger.Warn("no app secret configured, webhooks go out unsigned")
	}
	if a.config.WebhookURL == "" {
		a.logger.Warn("no webhook URL configured, webhooks are recorded but not sent")
	}

	// Request contexts derive from this one, so canceling it also ends the
	// console's event streams, which Shutdown would otherwise wait on forever.
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()

	servers := []*http.Server{
		a.newServer(serveCtx, a.config.GraphAddr, a.GraphHandler),
		a.newServer(serveCtx, a.config.ConsoleAddr, a.ConsoleHandler),
	}
	listeners := make([]net.Listener, 0, len(servers))
	for _, server := range servers {
		listener, err := net.Listen("tcp", server.Addr)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return fmt.Errorf("listen on %s: %w", server.Addr, err)
		}
		listeners = append(listeners, listener)
	}
	a.logger.Info("wasim is running",
		"graph", listeners[0].Addr().String(),
		"console", listeners[1].Addr().String(),
		"webhook_url", a.config.WebhookURL,
	)

	var background sync.WaitGroup
	background.Go(func() { a.dispatcher.Run(serveCtx) })
	if a.config.VerifyOnStart {
		background.Go(func() { a.runStartupHandshake(serveCtx) })
	}

	failures := make(chan error, len(servers))
	for index, server := range servers {
		listener := listeners[index]
		background.Go(func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				failures <- fmt.Errorf("serve %s: %w", server.Addr, err)
			}
		})
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-failures:
	}
	stopServing()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			a.logger.Warn("shut down server", "addr", server.Addr, "error", err)
		}
	}
	background.Wait()
	return runErr
}

func (a *App) newServer(ctx context.Context, addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

func (a *App) runStartupHandshake(ctx context.Context) {
	for attempt := 1; attempt <= handshakeAttempts; attempt++ {
		result, err := a.Service.VerifyWebhook(ctx)
		if err != nil {
			a.logger.Warn("webhook verification skipped", "error", err)
			return
		}
		if result.Verified {
			a.logger.Info("webhook verified")
			return
		}
		a.logger.Debug("webhook not verified yet", "attempt", attempt, "status", result.Status, "error", result.Error)
		select {
		case <-ctx.Done():
			return
		case <-time.After(handshakeInterval):
		}
	}
	a.logger.Warn("webhook verification failed, the consumer did not echo the challenge")
}
