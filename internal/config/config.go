// Package config reads the settings from flags and environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
)

// noAutoStatuses turns automatic statuses off. An empty value cannot, because an
// empty environment variable means "use the default".
const noAutoStatuses = "none"

// Config is the full application configuration.
type Config struct {
	GraphAddr   string
	ConsoleAddr string
	PublicURL   string

	BusinessAccountID  string
	PhoneNumberID      string
	DisplayPhoneNumber string
	AccessTokens       []string

	WebhookURL        string
	AppSecret         string
	VerifyToken       string
	VerifyOnStart     bool
	EscapeUnicode     bool
	WebhookWorkers    int
	WebhookAttempts   int
	WebhookTimeout    time.Duration
	WebhookRetryBase  time.Duration
	WebhookRetryLimit time.Duration

	DatabasePath string
	MediaDir     string
	SeedFile     string

	AutoStatuses       []domain.MessageStatus
	StatusDelay        time.Duration
	EnforceWindow      bool
	WindowErrorChannel cloudapi.Channel

	LogLevel slog.Level

	ShowVersion bool
}

// Load reads configuration. A flag beats its environment variable, which beats
// the default.
func Load(args []string, getenv func(string) string, output io.Writer) (Config, error) {
	var config Config
	var (
		accessTokens, autoStatuses, windowChannel, logLevel string
	)
	flags := flag.NewFlagSet("wasim", flag.ContinueOnError)
	flags.SetOutput(output)
	source := envSource{getenv: getenv, flags: flags}

	source.str(&config.GraphAddr, "graph-addr", "WASIM_GRAPH_ADDR", "127.0.0.1:7070", "listen address of the fake Graph API")
	source.str(&config.ConsoleAddr, "console-addr", "WASIM_CONSOLE_ADDR", "127.0.0.1:7071", "listen address of the console (no auth, keep it private)")
	source.str(&config.PublicURL, "public-url", "WASIM_PUBLIC_URL", "http://localhost:7070", "base URL the consumer reaches the Graph API at, used in media and paging URLs")

	source.str(&config.BusinessAccountID, "business-account-id", "WASIM_BUSINESS_ACCOUNT_ID", "200000000000001", "simulated WhatsApp Business Account id")
	source.str(&config.PhoneNumberID, "phone-number-id", "WASIM_PHONE_NUMBER_ID", "100000000000001", "simulated business phone number id")
	source.str(&config.DisplayPhoneNumber, "display-phone-number", "WASIM_DISPLAY_PHONE_NUMBER", "15550001234", "simulated business phone number, digits only")
	source.str(&accessTokens, "access-tokens", "WASIM_ACCESS_TOKENS", "", "comma-separated accepted bearer tokens, empty accepts any non-empty token")

	source.str(&config.WebhookURL, "webhook-url", "WASIM_WEBHOOK_URL", "", "consumer endpoint that receives webhooks, empty disables delivery")
	source.str(&config.AppSecret, "app-secret", "WASIM_APP_SECRET", "", "secret for X-Hub-Signature-256, empty sends webhooks unsigned")
	source.str(&config.VerifyToken, "verify-token", "WASIM_VERIFY_TOKEN", "wasim-verify-token", "token sent in the verification handshake")
	source.boolean(&config.VerifyOnStart, "verify-on-start", "WASIM_VERIFY_ON_START", false, "run the verification handshake at startup")
	source.boolean(&config.EscapeUnicode, "escape-unicode", "WASIM_ESCAPE_UNICODE", true, `send non-ASCII characters as \uXXXX in webhook bodies`)
	source.integer(&config.WebhookWorkers, "webhook-workers", "WASIM_WEBHOOK_WORKERS", 4, "concurrent webhook deliveries")
	source.integer(&config.WebhookAttempts, "webhook-attempts", "WASIM_WEBHOOK_ATTEMPTS", 8, "delivery attempts before giving up on a webhook")
	source.duration(&config.WebhookTimeout, "webhook-timeout", "WASIM_WEBHOOK_TIMEOUT", 10*time.Second, "timeout of one delivery attempt")
	source.duration(&config.WebhookRetryBase, "webhook-retry-base", "WASIM_WEBHOOK_RETRY_BASE", time.Second, "first backoff delay, doubled on each attempt")
	source.duration(&config.WebhookRetryLimit, "webhook-retry-limit", "WASIM_WEBHOOK_RETRY_LIMIT", 5*time.Minute, "longest backoff delay")

	source.str(&config.DatabasePath, "db-path", "WASIM_DB_PATH", "./wasim.db", `SQLite file, or ":memory:" for state that resets on restart`)
	source.str(&config.MediaDir, "media-dir", "WASIM_MEDIA_DIR", "./wasim-media", "directory for media files")
	source.str(&config.SeedFile, "seed-file", "WASIM_SEED_FILE", "", "JSON file with templates and contacts, empty uses the built-in seed")

	source.str(&autoStatuses, "auto-statuses", "WASIM_AUTO_STATUSES", "sent,delivered", "statuses reported automatically after a send, in order, or \"none\"")
	source.duration(&config.StatusDelay, "status-delay", "WASIM_STATUS_DELAY", 200*time.Millisecond, "time between automatic statuses")
	source.boolean(&config.EnforceWindow, "enforce-window", "WASIM_ENFORCE_WINDOW", true, "fail free-form messages outside the 24h customer service window")
	source.str(&windowChannel, "window-error-channel", "WASIM_WINDOW_ERROR_CHANNEL", string(cloudapi.ChannelAsync), "how the 24h window failure arrives: async or sync")

	source.str(&logLevel, "log-level", "WASIM_LOG_LEVEL", "info", "debug, info, warn or error")
	flags.BoolVar(&config.ShowVersion, "version", false, "print the version and exit")

	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if source.err != nil {
		return Config{}, source.err
	}

	config.AccessTokens = splitList(accessTokens)
	config.WindowErrorChannel = cloudapi.Channel(windowChannel)
	var err error
	if config.AutoStatuses, err = parseAutoStatuses(autoStatuses); err != nil {
		return Config{}, err
	}
	if err := config.LogLevel.UnmarshalText([]byte(logLevel)); err != nil {
		return Config{}, fmt.Errorf("log level: %w", err)
	}
	config.PublicURL = strings.TrimRight(config.PublicURL, "/")
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) validate() error {
	var problems []error
	if !c.WindowErrorChannel.Valid() {
		problems = append(problems, errors.New("window error channel must be sync or async"))
	}
	if !isAbsoluteHTTPURL(c.PublicURL) {
		problems = append(problems, fmt.Errorf("public URL %q must be an absolute http(s) URL", c.PublicURL))
	}
	if c.WebhookURL != "" && !isAbsoluteHTTPURL(c.WebhookURL) {
		problems = append(problems, fmt.Errorf("webhook URL %q must be an absolute http(s) URL", c.WebhookURL))
	}
	if c.WebhookWorkers < 1 {
		problems = append(problems, errors.New("webhook workers must be at least 1"))
	}
	if c.WebhookAttempts < 1 {
		problems = append(problems, errors.New("webhook attempts must be at least 1"))
	}
	if c.WebhookTimeout <= 0 || c.WebhookRetryBase <= 0 || c.WebhookRetryLimit <= 0 {
		problems = append(problems, errors.New("webhook timeout and retry delays must be positive"))
	}
	if c.StatusDelay < 0 {
		problems = append(problems, errors.New("status delay cannot be negative"))
	}
	return errors.Join(problems...)
}

func isAbsoluteHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func splitList(value string) []string {
	items := []string{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseAutoStatuses(value string) ([]domain.MessageStatus, error) {
	statuses := []domain.MessageStatus{}
	if value == noAutoStatuses {
		return statuses, nil
	}
	for _, item := range splitList(value) {
		status, ok := domain.ParseOutboundStatus(item)
		if !ok || status == domain.StatusFailed {
			return nil, fmt.Errorf("auto statuses: %q must be sent, delivered or read", item)
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// envSource registers a flag whose default comes from an environment variable.
type envSource struct {
	getenv func(string) string
	flags  *flag.FlagSet
	err    error
}

func (s *envSource) str(target *string, name, env, fallback, usage string) {
	value := fallback
	if raw := s.getenv(env); raw != "" {
		value = raw
	}
	s.flags.StringVar(target, name, value, usage+" ("+env+")")
}

func (s *envSource) boolean(target *bool, name, env string, fallback bool, usage string) {
	value := fallback
	if raw := s.getenv(env); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			s.fail(env, raw, err)
		} else {
			value = parsed
		}
	}
	s.flags.BoolVar(target, name, value, usage+" ("+env+")")
}

func (s *envSource) integer(target *int, name, env string, fallback int, usage string) {
	value := fallback
	if raw := s.getenv(env); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			s.fail(env, raw, err)
		} else {
			value = parsed
		}
	}
	s.flags.IntVar(target, name, value, usage+" ("+env+")")
}

func (s *envSource) duration(target *time.Duration, name, env string, fallback time.Duration, usage string) {
	value := fallback
	if raw := s.getenv(env); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			s.fail(env, raw, err)
		} else {
			value = parsed
		}
	}
	s.flags.DurationVar(target, name, value, usage+" ("+env+")")
}

func (s *envSource) fail(env, raw string, err error) {
	s.err = errors.Join(s.err, fmt.Errorf("%s=%q: %w", env, raw, err))
}
