package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionFlagPrintsTheVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != version+"\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-webhook-url", "WASIM_WEBHOOK_URL", "-console-addr"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("usage does not mention %s", want)
		}
	}
}

func TestUnknownFlagsFail(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--no-such-flag"}, &stdout, &stderr); err == nil {
		t.Fatal("an unknown flag was accepted")
	}
}

func TestInvalidEnvironmentFails(t *testing.T) {
	t.Setenv("WASIM_WEBHOOK_WORKERS", "many")
	var stdout, stderr bytes.Buffer
	err := run(nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "WASIM_WEBHOOK_WORKERS") {
		t.Fatalf("error = %v", err)
	}
}

func TestStartupFailuresAreReported(t *testing.T) {
	t.Setenv("WASIM_SEED_FILE", t.TempDir()+"/missing.json")
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "seed") {
		t.Fatalf("error = %v", err)
	}
}
