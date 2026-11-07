package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
)

const (
	email    = "ana@example.com"
	password = "correct horse"
)

// The real iteration count makes each hash cost tens of milliseconds, and far
// more under the race detector. Nothing here depends on the count.
func TestMain(m *testing.M) {
	hashIterations = 1000
	os.Exit(m.Run())
}

func newUsers(t *testing.T) *Users {
	t.Helper()
	database, err := store.Open(context.Background(), store.MemoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewUsers(database)
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$"+strconv.Itoa(hashIterations)+"$") {
		t.Errorf("hash = %q", hash)
	}
	if ok, err := checkPassword(hash, password); err != nil || !ok {
		t.Errorf("the right password does not match: %v", err)
	}
	if ok, _ := checkPassword(hash, password+"!"); ok {
		t.Error("a wrong password matches")
	}
	other, _ := hashPassword(password)
	if other == hash {
		t.Error("two hashes of the same password share a salt")
	}
}

func TestMalformedHashesAreRejected(t *testing.T) {
	for _, hash := range []string{"", "plain", "bcrypt$1$AA$AA", "pbkdf2-sha256$x$AA$AA", "pbkdf2-sha256$1$!!$AA", "pbkdf2-sha256$1$AA$"} {
		if _, err := checkPassword(hash, password); !errors.Is(err, errMalformedHash) {
			t.Errorf("%q: err = %v", hash, err)
		}
	}
}

func TestDecoyHashMatchesNothing(t *testing.T) {
	ok, err := checkPassword(decoyHash, "")
	if err != nil || ok {
		t.Fatalf("decoy: ok=%v err=%v", ok, err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail("  Ana@Example.COM ")
	if err != nil || got != email {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "ana", "Ana <ana@example.com>", "ana@example.com, bia@example.com"} {
		if _, err := NormalizeEmail(bad); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestAddValidatesAndRejectsDuplicates(t *testing.T) {
	ctx := context.Background()
	users := newUsers(t)
	if _, err := users.Add(ctx, email, "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("short password: %v", err)
	}
	if _, err := users.Add(ctx, "not an email", password); !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("bad email: %v", err)
	}
	if _, err := users.Add(ctx, "ANA@example.com", password); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Add(ctx, email, password); !errors.Is(err, store.ErrUserExists) {
		t.Errorf("duplicate: %v", err)
	}
	list, err := users.List(ctx)
	if err != nil || len(list) != 1 || list[0].Email != email {
		t.Errorf("list = %+v, %v", list, err)
	}
	if exists, err := users.Exists(ctx, "Ana@Example.com"); err != nil || !exists {
		t.Errorf("exists = %v, %v", exists, err)
	}
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	users := newUsers(t)
	if _, err := users.Add(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Verify(ctx, "ANA@example.com", password); err != nil {
		t.Errorf("right password: %v", err)
	}
	// A second call takes the cached path and must agree.
	if _, err := users.Verify(ctx, email, password); err != nil {
		t.Errorf("cached right password: %v", err)
	}
	for name, attempt := range map[string][2]string{
		"wrong password": {email, "wrong password"},
		"unknown email":  {"bia@example.com", password},
		"invalid email":  {"bia", password},
	} {
		if _, err := users.Verify(ctx, attempt[0], attempt[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestVerifyForgetsAnOldPassword(t *testing.T) {
	ctx := context.Background()
	users := newUsers(t)
	if _, err := users.Add(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Verify(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	if err := users.SetPassword(ctx, email, "a new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Verify(ctx, email, password); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the cached old password still works: %v", err)
	}
	if _, err := users.Verify(ctx, email, "a new password"); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestSetPasswordAndRemoveNeedAnExistingUser(t *testing.T) {
	ctx := context.Background()
	users := newUsers(t)
	if err := users.SetPassword(ctx, email, password); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("passwd: %v", err)
	}
	if err := users.Remove(ctx, email); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("remove: %v", err)
	}
}

func check(t *testing.T, guard *Guard, prepare func(*http.Request)) Access {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/info", nil)
	if prepare != nil {
		prepare(request)
	}
	access, err := guard.Check(request)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

func withCookie(cookie *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value}) }
}

func login(t *testing.T, guard *Guard) *http.Cookie {
	t.Helper()
	cookie, err := guard.Login(httptest.NewRequest(http.MethodPost, "/api/login", nil), email, password)
	if err != nil {
		t.Fatal(err)
	}
	return cookie
}

func TestGuardIsOpenWithoutUsers(t *testing.T) {
	guard := NewGuard(newUsers(t))
	access := check(t, guard, nil)
	if !access.Allowed() || access.Required {
		t.Errorf("access = %+v", access)
	}
}

func TestGuardSessions(t *testing.T) {
	ctx := context.Background()
	users := newUsers(t)
	if _, err := users.Add(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	guard := NewGuard(users)

	if access := check(t, guard, nil); access.Allowed() {
		t.Fatalf("anonymous access = %+v", access)
	}
	if _, err := guard.Login(httptest.NewRequest(http.MethodPost, "/", nil), email, "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong login: %v", err)
	}

	cookie := login(t, guard)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure || cookie.Path != "/" {
		t.Errorf("cookie = %+v", cookie)
	}
	access := check(t, guard, withCookie(cookie))
	if !access.Allowed() || access.Email != email || !access.ViaSession {
		t.Errorf("session access = %+v", access)
	}
	if access := check(t, guard, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "forged"}) }); access.Allowed() {
		t.Error("a forged token is accepted")
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	withCookie(cookie)(logoutRequest)
	if cleared := guard.Logout(logoutRequest); cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("logout cookie = %+v", cleared)
	}
	if access := check(t, guard, withCookie(cookie)); access.Allowed() {
		t.Error("the session outlived the logout")
	}
}

func TestSessionsEnd(t *testing.T) {
	ctx := context.Background()
	for name, end := range map[string]func(*Users, *Guard){
		"after their lifetime": func(_ *Users, g *Guard) {
			g.now = func() time.Time { return time.Now().Add(SessionLifetime + time.Second) }
		},
		"when the password changes": func(u *Users, _ *Guard) {
			if err := u.SetPassword(ctx, email, "a new password"); err != nil {
				t.Fatal(err)
			}
		},
		"when the user is removed": func(u *Users, _ *Guard) {
			if err := u.Remove(ctx, email); err != nil {
				t.Fatal(err)
			}
			// Keep a user so the console still requires a login.
			if _, err := u.Add(ctx, "bia@example.com", password); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			users := newUsers(t)
			if _, err := users.Add(ctx, email, password); err != nil {
				t.Fatal(err)
			}
			guard := NewGuard(users)
			cookie := login(t, guard)
			end(users, guard)
			if access := check(t, guard, withCookie(cookie)); access.Allowed() {
				t.Errorf("the session is still valid: %+v", access)
			}
		})
	}
}

func TestGuardAcceptsBasicCredentials(t *testing.T) {
	ctx := context.Background()
	users := newUsers(t)
	if _, err := users.Add(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	guard := NewGuard(users)
	access := check(t, guard, func(r *http.Request) { r.SetBasicAuth(email, password) })
	if !access.Allowed() || access.Email != email || access.ViaSession {
		t.Errorf("basic access = %+v", access)
	}
	if access := check(t, guard, func(r *http.Request) { r.SetBasicAuth(email, "wrong password") }); access.Allowed() {
		t.Error("a wrong Basic password is accepted")
	}
}
