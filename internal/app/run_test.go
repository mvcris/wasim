package app_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/app"
	"github.com/mvcris/wasim/internal/auth"
	"github.com/mvcris/wasim/internal/config"
	"github.com/mvcris/wasim/internal/store"
)

type logBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *logBuffer) waitFor(t *testing.T, text string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if logs := b.String(); strings.Contains(logs, text) {
			return logs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("log never contained %q:\n%s", text, b.String())
	return ""
}

func runConfig(t *testing.T, adjust func(*config.Config)) config.Config {
	t.Helper()
	cfg, err := config.Load(nil, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg.GraphAddr = "127.0.0.1:0"
	cfg.ConsoleAddr = "127.0.0.1:0"
	cfg.DatabasePath = store.MemoryPath
	cfg.MediaDir = t.TempDir()
	if adjust != nil {
		adjust(&cfg)
	}
	return cfg
}

func startApp(t *testing.T, cfg config.Config, logs *logBuffer) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	application, err := app.New(ctx, cfg, "test", logger)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	return cancel, done
}

// stop cancels a running app and requires a clean shutdown.
func stop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

var addrPattern = regexp.MustCompile(`graph=(\S+) console=(\S+)`)

func TestRunServesBothPortsAndStopsOnCancel(t *testing.T) {
	logs := &logBuffer{}
	cancel, done := startApp(t, runConfig(t, nil), logs)
	match := addrPattern.FindStringSubmatch(logs.waitFor(t, "wasim is running"))
	if match == nil {
		t.Fatalf("no addresses in the log: %s", logs.String())
	}

	info, err := http.Get("http://" + match[2] + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	info.Body.Close()
	request, _ := http.NewRequest(http.MethodGet, "http://"+match[1]+"/v20.0/123", nil)
	request.Header.Set("Authorization", "Bearer token")
	graph, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	graph.Body.Close()
	if info.StatusCode != http.StatusOK || graph.StatusCode != http.StatusBadRequest {
		t.Errorf("console HTTP %d, graph HTTP %d", info.StatusCode, graph.StatusCode)
	}

	// An open event stream must not hold the shutdown.
	stream, err := http.Get("http://" + match[2] + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunWarnsAboutAMissingWebhookSetup(t *testing.T) {
	t.Run("no webhook URL", func(t *testing.T) {
		logs := &logBuffer{}
		cancel, done := startApp(t, runConfig(t, nil), logs)
		logs.waitFor(t, "webhooks are recorded but not sent")
		stop(t, cancel, done)
	})
	t.Run("no app secret", func(t *testing.T) {
		logs := &logBuffer{}
		cancel, done := startApp(t, runConfig(t, func(cfg *config.Config) { cfg.WebhookURL = "http://127.0.0.1:1/webhook" }), logs)
		logs.waitFor(t, "webhooks go out unsigned")
		stop(t, cancel, done)
	})
}

func TestRunFailsWhenAPortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	cfg := runConfig(t, func(cfg *config.Config) { cfg.ConsoleAddr = taken.Addr().String() })

	cancel, done := startApp(t, cfg, &logBuffer{})
	defer cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "listen on") {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going without its console port")
	}
}

func TestVerifyOnStart(t *testing.T) {
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("hub.verify_token") != "start-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, r.URL.Query().Get("hub.challenge"))
	}))
	defer consumer.Close()
	logs := &logBuffer{}
	cancel, done := startApp(t, runConfig(t, func(cfg *config.Config) {
		cfg.WebhookURL = consumer.URL
		cfg.VerifyToken = "start-token"
		cfg.VerifyOnStart = true
	}), logs)

	logs.waitFor(t, "webhook verified")
	stop(t, cancel, done)
}

func TestVerifyOnStartWithoutAURLIsSkipped(t *testing.T) {
	logs := &logBuffer{}
	cancel, done := startApp(t, runConfig(t, func(cfg *config.Config) { cfg.VerifyOnStart = true }), logs)
	logs.waitFor(t, "webhook verification skipped")
	stop(t, cancel, done)
}

func TestNewFailsOnABadSeed(t *testing.T) {
	cfg := runConfig(t, func(cfg *config.Config) { cfg.SeedFile = t.TempDir() + "/missing.json" })
	if _, err := app.New(context.Background(), cfg, "test", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("New accepted a missing seed file")
	}
}

func TestNewFailsOnAnUnusableDatabase(t *testing.T) {
	cfg := runConfig(t, func(cfg *config.Config) { cfg.DatabasePath = t.TempDir() + "/missing-dir/wasim.db" })
	if _, err := app.New(context.Background(), cfg, "test", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("New accepted a database in a directory that does not exist")
	}
}

// A user added by `wasim user add`, a separate process with its own connection
// to the file, protects a console that is already running.
func TestAUserAddedWhileRunningLocksTheConsole(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "wasim.db")
	logs := &logBuffer{}
	cancel, done := startApp(t, runConfig(t, func(cfg *config.Config) { cfg.DatabasePath = dbPath }), logs)
	running := logs.waitFor(t, "wasim is running")
	if !strings.Contains(running, `console_login="off (no users)"`) {
		t.Errorf("startup log does not say the console is open: %s", running)
	}
	consoleURL := "http://" + addrPattern.FindStringSubmatch(running)[2]

	status := func(request *http.Request) int {
		t.Helper()
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	anonymous, _ := http.NewRequest(http.MethodGet, consoleURL+"/api/contacts", nil)
	if got := status(anonymous); got != http.StatusOK {
		t.Fatalf("before any user: HTTP %d", got)
	}

	other, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.NewUsers(other).Add(ctx, "ana@example.com", "correct horse"); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()

	anonymous, _ = http.NewRequest(http.MethodGet, consoleURL+"/api/contacts", nil)
	if got := status(anonymous); got != http.StatusUnauthorized {
		t.Errorf("after adding a user: HTTP %d", got)
	}
	authorized, _ := http.NewRequest(http.MethodGet, consoleURL+"/api/contacts", nil)
	authorized.SetBasicAuth("ana@example.com", "correct horse")
	if got := status(authorized); got != http.StatusOK {
		t.Errorf("with credentials: HTTP %d", got)
	}
	stop(t, cancel, done)
}
