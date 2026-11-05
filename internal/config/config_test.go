package config

import (
	"io"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/cloudapi"
	"github.com/mvcris/wasim/internal/domain"
)

func environment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDefaultsAreValidAndLocalOnly(t *testing.T) {
	config, err := Load(nil, environment(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if config.GraphAddr != "127.0.0.1:7070" || config.ConsoleAddr != "127.0.0.1:7071" {
		t.Errorf("addresses = %s, %s", config.GraphAddr, config.ConsoleAddr)
	}
	if config.DatabasePath != "./wasim.db" {
		t.Errorf("DatabasePath = %q", config.DatabasePath)
	}
	if !config.EnforceWindow || !config.EscapeUnicode || config.WindowErrorChannel != cloudapi.ChannelAsync {
		t.Errorf("config = %+v", config)
	}
	if len(config.AutoStatuses) != 2 || config.AutoStatuses[1] != domain.StatusDelivered {
		t.Errorf("AutoStatuses = %v", config.AutoStatuses)
	}
}

func TestFlagsBeatEnvironment(t *testing.T) {
	config, err := Load(
		[]string{"--graph-addr", ":1"},
		environment(map[string]string{"WASIM_GRAPH_ADDR": ":2", "WASIM_CONSOLE_ADDR": ":3"}),
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.GraphAddr != ":1" {
		t.Errorf("GraphAddr = %q, want the flag", config.GraphAddr)
	}
	if config.ConsoleAddr != ":3" {
		t.Errorf("ConsoleAddr = %q, want the environment value", config.ConsoleAddr)
	}
}

func TestEnvironmentParsing(t *testing.T) {
	config, err := Load(nil, environment(map[string]string{
		"WASIM_ACCESS_TOKENS":   "one, two,,",
		"WASIM_STATUS_DELAY":    "1s",
		"WASIM_ENFORCE_WINDOW":  "false",
		"WASIM_PUBLIC_URL":      "http://wasim:7070/",
		"WASIM_AUTO_STATUSES":   "none",
		"WASIM_WEBHOOK_WORKERS": "9",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.AutoStatuses) != 0 {
		t.Errorf("AutoStatuses = %v", config.AutoStatuses)
	}
	if len(config.AccessTokens) != 2 || config.AccessTokens[1] != "two" {
		t.Errorf("AccessTokens = %v", config.AccessTokens)
	}
	if config.StatusDelay != time.Second || config.EnforceWindow || config.WebhookWorkers != 9 {
		t.Errorf("config = %+v", config)
	}
	if config.PublicURL != "http://wasim:7070" {
		t.Errorf("PublicURL = %q, want no trailing slash", config.PublicURL)
	}
}

func TestInvalidSettingsAreRefused(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"bad duration", "WASIM_STATUS_DELAY", "soon"},
		{"bad integer", "WASIM_WEBHOOK_WORKERS", "many"},
		{"bad boolean", "WASIM_ENFORCE_WINDOW", "maybe"},
		{"no workers", "WASIM_WEBHOOK_WORKERS", "0"},
		{"bad channel", "WASIM_WINDOW_ERROR_CHANNEL", "pigeon"},
		{"relative public URL", "WASIM_PUBLIC_URL", "localhost:7070"},
		{"relative webhook URL", "WASIM_WEBHOOK_URL", "/webhook"},
		{"failed cannot be automatic", "WASIM_AUTO_STATUSES", "sent,failed"},
		{"bad log level", "WASIM_LOG_LEVEL", "shout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(nil, environment(map[string]string{test.key: test.value}), io.Discard); err == nil {
				t.Errorf("%s=%s was accepted", test.key, test.value)
			}
		})
	}
}
