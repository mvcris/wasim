package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/mvcris/wasim/internal/domain"
)

const (
	// SessionCookie names the cookie that carries a console session.
	SessionCookie = "wasim_session"
	// SessionLifetime is how long a login lasts. Sessions live in memory, so a
	// restart ends them all sooner.
	SessionLifetime = 7 * 24 * time.Hour
	tokenBytes      = 32
)

// Guard decides whether a console request comes from a logged-in user. A
// browser holds a session cookie from Login; a script sends HTTP Basic
// credentials on every request instead.
type Guard struct {
	users *Users
	now   func() time.Time

	mu       sync.Mutex
	sessions map[string]session
}

// A session remembers the password hash it was created under, so changing the
// password ends it, even when `wasim user passwd` runs in another process.
type session struct {
	email        string
	passwordHash string
	expires      time.Time
}

// NewGuard returns a guard over users.
func NewGuard(users *Users) *Guard {
	return &Guard{users: users, now: time.Now, sessions: map[string]session{}}
}

// Access is the outcome of checking a request.
type Access struct {
	// Email is the logged-in user, empty when nobody is.
	Email string
	// ViaSession says the user logged in through the login page, so logging
	// out means something.
	ViaSession bool
	// Required says users exist, so an anonymous request must be refused.
	Required bool
}

// Allowed reports whether the request may proceed.
func (a Access) Allowed() bool {
	return a.Email != "" || !a.Required
}

// Check identifies who made the request.
func (g *Guard) Check(r *http.Request) (Access, error) {
	ctx := r.Context()
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		email, err := g.resolveSession(ctx, cookie.Value)
		if err != nil {
			return Access{}, err
		}
		if email != "" {
			return Access{Email: email, ViaSession: true, Required: true}, nil
		}
	}
	if email, password, ok := r.BasicAuth(); ok {
		user, err := g.users.Verify(ctx, email, password)
		if err == nil {
			return Access{Email: user.Email, Required: true}, nil
		}
		if !errors.Is(err, ErrInvalidCredentials) {
			return Access{}, err
		}
	}
	count, err := g.users.Count(ctx)
	if err != nil {
		return Access{}, err
	}
	return Access{Required: count > 0}, nil
}

// resolveSession returns the session's user, or "" if the token is unknown,
// expired, or belongs to a user who was removed or changed password.
func (g *Guard) resolveSession(ctx context.Context, token string) (string, error) {
	g.mu.Lock()
	current, ok := g.sessions[token]
	g.mu.Unlock()
	if !ok {
		return "", nil
	}
	if !g.now().Before(current.expires) {
		g.forget(token)
		return "", nil
	}
	user, err := g.users.lookup(ctx, current.email)
	if errors.Is(err, domain.ErrNotFound) {
		g.forget(token)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if user.PasswordHash != current.passwordHash {
		g.forget(token)
		return "", nil
	}
	return user.Email, nil
}

func (g *Guard) forget(token string) {
	g.mu.Lock()
	delete(g.sessions, token)
	g.mu.Unlock()
}

// Login checks the credentials and returns the cookie of a new session.
func (g *Guard) Login(r *http.Request, email, password string) (*http.Cookie, error) {
	user, err := g.users.Verify(r.Context(), email, password)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := g.now()
	expires := now.Add(SessionLifetime)

	g.mu.Lock()
	for existing, stale := range g.sessions {
		if !now.Before(stale.expires) {
			delete(g.sessions, existing)
		}
	}
	g.sessions[token] = session{email: user.Email, passwordHash: user.PasswordHash, expires: expires}
	g.mu.Unlock()
	return sessionCookie(r, token, expires), nil
}

// Logout ends the request's session, if any, and returns a cookie that clears
// it in the browser.
func (g *Guard) Logout(r *http.Request) *http.Cookie {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		g.forget(cookie.Value)
	}
	cookie := sessionCookie(r, "", time.Unix(0, 0))
	cookie.MaxAge = -1
	return cookie
}

// sessionCookie is unreadable from scripts and never sent by other sites'
// requests. It is Secure only behind TLS, because the console usually runs on
// plain http://localhost.
func sessionCookie(r *http.Request, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	}
}
