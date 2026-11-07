package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvcris/wasim/internal/auth"
	"github.com/mvcris/wasim/internal/store"
)

type userRun struct {
	stdout, stderr string
	err            error
}

// runUserWith runs `wasim user` against dbPath. secrets answers the terminal
// prompts in order; nil means stdin is not a terminal.
func runUserWith(t *testing.T, dbPath, stdin string, secrets []string, args ...string) userRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	con := newTerminal(strings.NewReader(stdin), &stdout, &stderr, func(name string) string {
		if name == "WASIM_DB_PATH" {
			return dbPath
		}
		return ""
	})
	if secrets != nil {
		con.readSecret = func(string) (string, error) {
			if len(secrets) == 0 {
				t.Fatal("asked for more secrets than expected")
			}
			next := secrets[0]
			secrets = secrets[1:]
			return next, nil
		}
	}
	err := runUser(context.Background(), args, con)
	return userRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func verify(t *testing.T, dbPath, email, password string) error {
	t.Helper()
	database, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = auth.NewUsers(database).Verify(context.Background(), email, password)
	return err
}

func TestUserLifecycle(t *testing.T) {
	db := filepath.Join(t.TempDir(), "wasim.db")

	added := runUserWith(t, db, "", []string{"correct horse", "correct horse"}, "add", "Ana@Example.com")
	if added.err != nil {
		t.Fatal(added.err)
	}
	if !strings.Contains(added.stdout, "Added ana@example.com to "+db) || !strings.Contains(added.stdout, "now asks for a login") {
		t.Errorf("stdout = %q", added.stdout)
	}
	if err := verify(t, db, "ana@example.com", "correct horse"); err != nil {
		t.Fatalf("the new user cannot log in: %v", err)
	}

	again := runUserWith(t, db, "", []string{}, "add", "ana@example.com")
	if again.err == nil || !strings.Contains(again.err.Error(), "already exists") {
		t.Errorf("duplicate: %v", again.err)
	}

	scripted := runUserWith(t, db, "battery staple\n", nil, "add", "bia@example.com", "--password-stdin")
	if scripted.err != nil || strings.Contains(scripted.stdout, "now asks") {
		t.Fatalf("stdin add: %v, %q", scripted.err, scripted.stdout)
	}
	if err := verify(t, db, "bia@example.com", "battery staple"); err != nil {
		t.Fatalf("the password from stdin was not stored as given: %v", err)
	}

	listed := runUserWith(t, db, "", nil, "list", "--db-path", db)
	if listed.err != nil || !strings.Contains(listed.stdout, "EMAIL") || !strings.Contains(listed.stdout, "ana@example.com") || !strings.Contains(listed.stdout, "bia@example.com") {
		t.Errorf("list: %v, %q", listed.err, listed.stdout)
	}

	changed := runUserWith(t, db, "", []string{"a new password", "a new password"}, "passwd", "ana@example.com")
	if changed.err != nil {
		t.Fatal(changed.err)
	}
	if err := verify(t, db, "ana@example.com", "a new password"); err != nil {
		t.Errorf("new password: %v", err)
	}

	for _, email := range []string{"ana@example.com", "bia@example.com"} {
		if removed := runUserWith(t, db, "", nil, "remove", email); removed.err != nil {
			t.Fatal(removed.err)
		}
	}
	empty := runUserWith(t, db, "", nil, "list")
	if !strings.Contains(empty.stdout, "No users") {
		t.Errorf("list after removing everyone = %q", empty.stdout)
	}
}

func TestUserPasswordPrompts(t *testing.T) {
	db := filepath.Join(t.TempDir(), "wasim.db")
	for name, secrets := range map[string][]string{
		"mismatch":  {"correct horse", "correct hose"},
		"too short": {"short"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := runUserWith(t, db, "", secrets, "add", "ana@example.com"); got.err == nil {
				t.Error("the user was added")
			}
		})
	}
	notTerminal := runUserWith(t, db, "", nil, "add", "ana@example.com")
	if notTerminal.err == nil || !strings.Contains(notTerminal.err.Error(), "--password-stdin") {
		t.Errorf("without a terminal: %v", notTerminal.err)
	}
	emptyStdin := runUserWith(t, db, "", nil, "add", "ana@example.com", "--password-stdin")
	if emptyStdin.err == nil {
		t.Error("an empty stdin was accepted as a password")
	}
}

func TestUserCommandErrors(t *testing.T) {
	db := filepath.Join(t.TempDir(), "wasim.db")
	for name, args := range map[string][]string{
		"no command":       {},
		"unknown command":  {"rename", "ana@example.com"},
		"missing email":    {"add"},
		"extra argument":   {"remove", "ana@example.com", "bia@example.com"},
		"stdin for remove": {"remove", "ana@example.com", "--password-stdin"},
		"unknown flag":     {"list", "--nope"},
	} {
		t.Run(name, func(t *testing.T) {
			got := runUserWith(t, db, "", nil, args...)
			if !errors.Is(got.err, errUsage) || !strings.Contains(got.stderr, "Usage: wasim user") {
				t.Errorf("err = %v, stderr = %q", got.err, got.stderr)
			}
		})
	}
	if got := runUserWith(t, db, "", nil, "remove", "ana@example.com"); got.err == nil || !strings.Contains(got.err.Error(), "no user ana@example.com") {
		t.Errorf("remove a missing user: %v", got.err)
	}
	if got := runUserWith(t, db, "", nil, "add", "not-an-email"); !errors.Is(got.err, auth.ErrInvalidEmail) {
		t.Errorf("invalid email: %v", got.err)
	}
	if got := runUserWith(t, ":memory:", "", nil, "list"); got.err == nil || !strings.Contains(got.err.Error(), "database file") {
		t.Errorf("in-memory database: %v", got.err)
	}
	if got := runUserWith(t, db, "", nil, "--help"); got.err != nil || !strings.Contains(got.stderr, "Commands:") {
		t.Errorf("help: %v", got.err)
	}
}

func TestRunDispatchesTheUserCommand(t *testing.T) {
	t.Setenv("WASIM_DB_PATH", filepath.Join(t.TempDir(), "wasim.db"))
	var stdout, stderr bytes.Buffer
	if err := run([]string{"user", "list"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "No users") {
		t.Errorf("stdout = %q", stdout.String())
	}
}
