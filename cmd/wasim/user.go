package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/mvcris/wasim/internal/auth"
	"github.com/mvcris/wasim/internal/config"
	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
)

const userUsage = `Usage: wasim user <command> [flags]

Manages who can log in to the console. The console asks for a login as soon as
one user exists, and changes apply to a running wasim without a restart.

Commands:
  add EMAIL       add a user, asking for the password
  passwd EMAIL    change a user's password, ending their sessions
  remove EMAIL    remove a user, ending their sessions
  list            list the users

Flags:
  --db-path PATH      SQLite file of the wasim instance (WASIM_DB_PATH)
  --password-stdin    read the password from the first line of stdin, for
                      scripts (add and passwd)

Examples:
  wasim user add ana@example.com
  echo "$PASSWORD" | wasim user add ana@example.com --password-stdin
  docker exec -it wasim /wasim user add ana@example.com
`

// errUsage reports a malformed command line, after the usage was printed.
var errUsage = errors.New("invalid arguments, see the usage above")

// terminal is the command's view of the terminal, replaceable in tests.
type terminal struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	// readSecret reads a line without echoing it. It fails when stdin is not
	// a terminal.
	readSecret func(prompt string) (string, error)
}

func newTerminal(stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) terminal {
	return terminal{
		stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv,
		readSecret: func(prompt string) (string, error) {
			file, ok := stdin.(*os.File)
			// File descriptors are small integers, so the conversion is exact.
			if !ok || !term.IsTerminal(int(file.Fd())) { //nolint:gosec // see above
				return "", errors.New("stdin is not a terminal: pipe the password and add --password-stdin")
			}
			fmt.Fprint(stderr, prompt)
			secret, err := term.ReadPassword(int(file.Fd())) //nolint:gosec // see above
			fmt.Fprintln(stderr)
			return string(secret), err
		},
	}
}

func runUser(ctx context.Context, args []string, con terminal) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(con.stderr, userUsage)
		if len(args) == 0 {
			return errUsage
		}
		return nil
	}
	command, args := args[0], args[1:]

	flags := flag.NewFlagSet("wasim user "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dbPath := con.getenv(config.DatabasePathEnv)
	if dbPath == "" {
		dbPath = config.DefaultDatabasePath
	}
	flags.StringVar(&dbPath, "db-path", dbPath, "")
	passwordStdin := flags.Bool("password-stdin", false, "")
	positional, err := parseInterspersed(flags, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(con.stderr, userUsage)
		return nil
	}
	if err != nil {
		fmt.Fprintf(con.stderr, "%v\n\n%s", err, userUsage)
		return errUsage
	}

	wantArgs := map[string]int{"add": 1, "passwd": 1, "remove": 1, "list": 0}
	want, known := wantArgs[command]
	if !known {
		fmt.Fprintf(con.stderr, "unknown command %q\n\n%s", command, userUsage)
		return errUsage
	}
	if len(positional) != want {
		fmt.Fprintf(con.stderr, "%s takes %d argument(s), got %d\n\n%s", command, want, len(positional), userUsage)
		return errUsage
	}
	if *passwordStdin && command != "add" && command != "passwd" {
		fmt.Fprintf(con.stderr, "--password-stdin only applies to add and passwd\n\n%s", userUsage)
		return errUsage
	}
	if dbPath == store.MemoryPath {
		return errors.New("users need a database file: an in-memory database only exists inside the running wasim process")
	}

	database, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	users := auth.NewUsers(database)
	where := dbPath
	if absolute, err := filepath.Abs(dbPath); err == nil {
		where = absolute
	}

	switch command {
	case "add":
		return addUser(ctx, users, positional[0], *passwordStdin, where, con)
	case "passwd":
		return changePassword(ctx, users, positional[0], *passwordStdin, con)
	case "remove":
		return removeUser(ctx, users, positional[0], con)
	default:
		return listUsers(ctx, users, where, con)
	}
}

func addUser(ctx context.Context, users *auth.Users, email string, fromStdin bool, where string, con terminal) error {
	email, err := auth.NormalizeEmail(email)
	if err != nil {
		return err
	}
	// Fail on a taken email before asking for a password nobody will use.
	exists, err := users.Exists(ctx, email)
	if err != nil {
		return err
	}
	if exists {
		return errUserExists(email)
	}
	password, err := readNewPassword(fromStdin, con)
	if err != nil {
		return err
	}
	user, err := users.Add(ctx, email, password)
	if errors.Is(err, store.ErrUserExists) {
		return errUserExists(email)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(con.stdout, "Added %s to %s.\n", user.Email, where)
	if count, err := users.Count(ctx); err == nil && count == 1 {
		fmt.Fprintln(con.stdout, "The console now asks for a login.")
	}
	return nil
}

func changePassword(ctx context.Context, users *auth.Users, email string, fromStdin bool, con terminal) error {
	email, err := auth.NormalizeEmail(email)
	if err != nil {
		return err
	}
	password, err := readNewPassword(fromStdin, con)
	if err != nil {
		return err
	}
	if err := users.SetPassword(ctx, email, password); err != nil {
		return userError(email, err)
	}
	fmt.Fprintf(con.stdout, "Changed the password of %s. Their console sessions have ended.\n", email)
	return nil
}

func removeUser(ctx context.Context, users *auth.Users, email string, con terminal) error {
	email, err := auth.NormalizeEmail(email)
	if err != nil {
		return err
	}
	if err := users.Remove(ctx, email); err != nil {
		return userError(email, err)
	}
	fmt.Fprintf(con.stdout, "Removed %s.\n", email)
	if count, err := users.Count(ctx); err == nil && count == 0 {
		fmt.Fprintln(con.stdout, "No users are left: the console is open to anyone who reaches it.")
	}
	return nil
}

func listUsers(ctx context.Context, users *auth.Users, where string, con terminal) error {
	list, err := users.List(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(con.stdout, "No users in %s: the console is open to anyone who reaches it.\n", where)
		return nil
	}
	table := tabwriter.NewWriter(con.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "EMAIL\tCREATED")
	for _, user := range list {
		fmt.Fprintf(table, "%s\t%s\n", user.Email, user.CreatedAt.Local().Format(time.DateTime))
	}
	return table.Flush()
}

func errUserExists(email string) error {
	return fmt.Errorf("%s already exists, use wasim user passwd to change the password", email)
}

func userError(email string, err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("no user %s, see wasim user list", email)
	}
	return err
}

// readNewPassword asks twice on a terminal, so a typo cannot lock the user out.
func readNewPassword(fromStdin bool, con terminal) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(con.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read password: %w", err)
		}
		password := strings.TrimRight(line, "\r\n")
		if password == "" {
			return "", errors.New("no password on stdin")
		}
		return password, nil
	}
	password, err := con.readSecret("Password: ")
	if err != nil {
		return "", err
	}
	if len([]rune(password)) < auth.MinPasswordLength {
		return "", auth.ErrPasswordTooShort
	}
	confirmation, err := con.readSecret("Confirm password: ")
	if err != nil {
		return "", err
	}
	if confirmation != password {
		return "", errors.New("the passwords do not match")
	}
	return password, nil
}

// parseInterspersed accepts flags after positional arguments, as in
// "user add EMAIL --password-stdin", which the flag package alone stops at.
func parseInterspersed(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		args = flags.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
