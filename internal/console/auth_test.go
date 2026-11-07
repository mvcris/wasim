package console_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mvcris/wasim/internal/auth"
)

const (
	loginEmail    = "ana@example.com"
	loginPassword = "correct horse"
)

// authReply keeps what the redirect and cookie checks need.
type authReply struct {
	status   int
	location string
	cookies  []*http.Cookie
	header   http.Header
	body     string
}

// send does not follow redirects, so a test sees where the console sends a page.
func (f *fixture) send(method, path, body string, prepare func(*http.Request)) authReply {
	f.t.Helper()
	request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if prepare != nil {
		prepare(request)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	content, _ := io.ReadAll(response.Body)
	return authReply{
		status:   response.StatusCode,
		location: response.Header.Get("Location"),
		cookies:  response.Cookies(),
		header:   response.Header,
		body:     string(content),
	}
}

func (f *fixture) addUser() {
	f.t.Helper()
	if _, err := f.users.Add(context.Background(), loginEmail, loginPassword); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) login() *http.Cookie {
	f.t.Helper()
	got := f.send(http.MethodPost, "/api/login", `{"email":"`+loginEmail+`","password":"`+loginPassword+`"}`, nil)
	if got.status != http.StatusNoContent || len(got.cookies) != 1 {
		f.t.Fatalf("login: HTTP %d, cookies %v: %s", got.status, got.cookies, got.body)
	}
	return got.cookies[0]
}

func withSession(cookie *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value}) }
}

func TestTheConsoleIsOpenWithoutUsers(t *testing.T) {
	f := newFixture(t)
	if got := f.send(http.MethodGet, "/api/contacts", "", nil); got.status != http.StatusOK {
		t.Errorf("API: HTTP %d", got.status)
	}
	if got := f.send(http.MethodGet, "/login", "", nil); got.status != http.StatusSeeOther || got.location != "/" {
		t.Errorf("login page: HTTP %d to %q", got.status, got.location)
	}
}

func TestAnonymousRequestsAreRefusedOnceAUserExists(t *testing.T) {
	f := newFixture(t)
	f.addUser()

	api := f.send(http.MethodGet, "/api/contacts", "", nil)
	if api.status != http.StatusUnauthorized || !strings.Contains(api.body, "log in at /login") {
		t.Errorf("API: HTTP %d: %s", api.status, api.body)
	}
	if challenge := api.header.Get("WWW-Authenticate"); challenge != "" {
		t.Errorf("a browser request gets a Basic challenge, which opens the browser's own dialog: %q", challenge)
	}
	for _, path := range []string{"/", "/index.html", "/app.js"} {
		if got := f.send(http.MethodGet, path, "", nil); got.status != http.StatusSeeOther || got.location != "/login" {
			t.Errorf("%s: HTTP %d to %q", path, got.status, got.location)
		}
	}
	if got := f.send(http.MethodPost, "/api/reset", "", nil); got.status != http.StatusUnauthorized {
		t.Errorf("reset: HTTP %d", got.status)
	}
	for path, marker := range map[string]string{"/login": `id="login-form"`, "/login.js": "/api/login", "/style.css": ".login-card"} {
		if got := f.send(http.MethodGet, path, "", nil); got.status != http.StatusOK || !strings.Contains(got.body, marker) {
			t.Errorf("%s must stay public: HTTP %d", path, got.status)
		}
	}
}

func TestLoginLogout(t *testing.T) {
	f := newFixture(t)
	f.addUser()

	wrong := f.send(http.MethodPost, "/api/login", `{"email":"`+loginEmail+`","password":"nope nope"}`, nil)
	if wrong.status != http.StatusUnauthorized || len(wrong.cookies) != 0 || !strings.Contains(wrong.body, "wrong email or password") {
		t.Fatalf("wrong password: HTTP %d: %s", wrong.status, wrong.body)
	}

	cookie := f.login()
	if cookie.Name != auth.SessionCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie = %+v", cookie)
	}
	info := f.send(http.MethodGet, "/api/info", "", withSession(cookie))
	var body struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal([]byte(info.body), &body); err != nil || info.status != http.StatusOK || body.User != loginEmail {
		t.Errorf("info: HTTP %d: %s", info.status, info.body)
	}
	if got := f.send(http.MethodGet, "/", "", withSession(cookie)); got.status != http.StatusOK {
		t.Errorf("page: HTTP %d", got.status)
	}
	if got := f.send(http.MethodGet, "/login", "", withSession(cookie)); got.status != http.StatusSeeOther || got.location != "/" {
		t.Errorf("a logged-in user sees the login page: HTTP %d to %q", got.status, got.location)
	}

	logout := f.send(http.MethodPost, "/api/logout", "", withSession(cookie))
	if logout.status != http.StatusNoContent || len(logout.cookies) != 1 || logout.cookies[0].MaxAge >= 0 {
		t.Errorf("logout: HTTP %d, cookies %v", logout.status, logout.cookies)
	}
	if got := f.send(http.MethodGet, "/api/info", "", withSession(cookie)); got.status != http.StatusUnauthorized {
		t.Errorf("after logout: HTTP %d", got.status)
	}
}

func TestRemovingTheUserEndsTheSession(t *testing.T) {
	f := newFixture(t)
	f.addUser()
	cookie := f.login()
	if _, err := f.users.Add(context.Background(), "bia@example.com", loginPassword); err != nil {
		t.Fatal(err)
	}
	if err := f.users.Remove(context.Background(), loginEmail); err != nil {
		t.Fatal(err)
	}
	if got := f.send(http.MethodGet, "/api/contacts", "", withSession(cookie)); got.status != http.StatusUnauthorized {
		t.Errorf("HTTP %d", got.status)
	}
}

func TestScriptsUseBasicCredentials(t *testing.T) {
	f := newFixture(t)
	f.addUser()

	got := f.send(http.MethodPost, "/api/reset", "", func(r *http.Request) { r.SetBasicAuth(loginEmail, loginPassword) })
	if got.status != http.StatusNoContent {
		t.Errorf("right credentials: HTTP %d: %s", got.status, got.body)
	}
	wrong := f.send(http.MethodGet, "/api/contacts", "", func(r *http.Request) { r.SetBasicAuth(loginEmail, "wrong password") })
	if wrong.status != http.StatusUnauthorized || !strings.HasPrefix(wrong.header.Get("WWW-Authenticate"), "Basic ") {
		t.Errorf("wrong credentials: HTTP %d, challenge %q", wrong.status, wrong.header.Get("WWW-Authenticate"))
	}
	// Basic credentials never create a session, so the page has nothing to log out of.
	info := f.send(http.MethodGet, "/api/info", "", func(r *http.Request) { r.SetBasicAuth(loginEmail, loginPassword) })
	if strings.Contains(info.body, `"user"`) {
		t.Errorf("info = %s", info.body)
	}
}

func TestLoginFromAnotherSiteIsRefused(t *testing.T) {
	f := newFixture(t)
	f.addUser()
	got := f.send(http.MethodPost, "/api/login", `{"email":"`+loginEmail+`","password":"`+loginPassword+`"}`,
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
	if got.status != http.StatusForbidden || len(got.cookies) != 0 {
		t.Errorf("HTTP %d, cookies %v", got.status, got.cookies)
	}
}
