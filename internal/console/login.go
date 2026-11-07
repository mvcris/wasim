package console

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/mvcris/wasim/internal/auth"
)

type accessKey struct{}

// publicRoutes are what the login page needs before anyone has logged in.
var publicRoutes = map[string]bool{
	"GET /login":      true,
	"GET /login.js":   true,
	"GET /style.css":  true,
	"POST /api/login": true,
}

// requireLogin refuses anonymous requests once a console user exists. The API
// answers 401, so scripts see the problem; pages redirect to the login form.
func (h *Handler) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicRoutes[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		access, err := h.guard.Check(r)
		if err != nil {
			h.writeFailure(w, err)
			return
		}
		if access.Allowed() {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, access)))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			// Only a client that already sends credentials gets the Basic
			// challenge: a browser would answer it with its own login dialog
			// in front of the page's.
			if r.Header.Get("Authorization") != "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="wasim console", charset="UTF-8"`)
			}
			writeError(w, http.StatusUnauthorized, "authentication required: log in at /login, or send HTTP Basic credentials")
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	access, err := h.guard.Check(r)
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	// With no users, or a valid login, the form has nothing to do.
	if access.Allowed() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.ServeFileFS(w, r, h.static, "login.html")
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if !h.decodeJSON(w, r, &request) {
		return
	}
	cookie, err := h.guard.Login(r, request.Email, request.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		h.logger.Warn("console login failed", "email", request.Email, "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		h.writeFailure(w, err)
		return
	}
	http.SetCookie(w, cookie)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, h.guard.Logout(r))
	w.WriteHeader(http.StatusNoContent)
}
