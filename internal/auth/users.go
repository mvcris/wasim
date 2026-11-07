// Package auth guards the console with logins stored in the database. The
// console asks for one as soon as a user exists, and stays open while none does.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mvcris/wasim/internal/domain"
	"github.com/mvcris/wasim/internal/store"
)

// MinPasswordLength is the shortest password a user can have.
const MinPasswordLength = 8

var (
	// ErrInvalidCredentials reports an unknown email or a wrong password,
	// without saying which.
	ErrInvalidCredentials = errors.New("wrong email or password")
	// ErrInvalidEmail reports a value that is not a plain email address.
	ErrInvalidEmail = errors.New("not a valid email address")
	// ErrPasswordTooShort reports a password under MinPasswordLength characters.
	ErrPasswordTooShort = fmt.Errorf("password must have at least %d characters", MinPasswordLength)
)

// Users manages console logins.
type Users struct {
	store *store.Store
	now   func() time.Time

	// A hash costs a noticeable fraction of a second by design, and a script
	// sending Basic credentials pays it on every request. A credential that
	// already matched is remembered as a keyed digest of the password, valid
	// while the stored hash stays the same.
	mu       sync.Mutex
	cacheKey []byte
	verified map[string]verifiedPassword
}

type verifiedPassword struct {
	passwordHash string
	digest       []byte
}

// NewUsers returns the user manager backed by database.
func NewUsers(database *store.Store) *Users {
	key := make([]byte, sha256.Size)
	// crypto/rand.Read never fails on supported platforms.
	_, _ = rand.Read(key)
	return &Users{store: database, now: time.Now, cacheKey: key, verified: map[string]verifiedPassword{}}
}

// User is a console login as shown to an operator. It never carries the hash.
type User struct {
	Email     string
	CreatedAt time.Time
}

// NormalizeEmail trims and lowercases an email address and checks that it is a
// bare address, so logins do not depend on how it was typed.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", fmt.Errorf("%q: %w", email, ErrInvalidEmail)
	}
	return email, nil
}

func validatePassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	return nil
}

// Add creates a user. It fails with store.ErrUserExists if the email is taken.
func (u *Users) Add(ctx context.Context, email, password string) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := u.now()
	if err := u.store.InsertUser(ctx, store.User{Email: email, PasswordHash: hash, CreatedAt: now, UpdatedAt: now}); err != nil {
		return User{}, err
	}
	return User{Email: email, CreatedAt: now}, nil
}

// SetPassword replaces a user's password, which also ends their sessions.
func (u *Users) SetPassword(ctx context.Context, email, password string) error {
	email, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return u.store.UpdateUserPassword(ctx, email, hash, u.now())
}

// Remove deletes a user, which also ends their sessions.
func (u *Users) Remove(ctx context.Context, email string) error {
	email, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	return u.store.DeleteUser(ctx, email)
}

// List returns every user, by email.
func (u *Users) List(ctx context.Context) ([]User, error) {
	records, err := u.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(records))
	for _, record := range records {
		users = append(users, User{Email: record.Email, CreatedAt: record.CreatedAt})
	}
	return users, nil
}

// Exists reports whether a user has this email.
func (u *Users) Exists(ctx context.Context, email string) (bool, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return false, err
	}
	_, err = u.store.GetUser(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Count returns how many users exist. Zero means the console is open.
func (u *Users) Count(ctx context.Context) (int, error) {
	return u.store.CountUsers(ctx)
}

// lookup returns the stored user, read on every request so that a change made
// by `wasim user` in another process applies at once.
func (u *Users) lookup(ctx context.Context, email string) (store.User, error) {
	return u.store.GetUser(ctx, email)
}

// Verify checks a login and returns the stored user.
func (u *Users) Verify(ctx context.Context, email, password string) (store.User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return store.User{}, ErrInvalidCredentials
	}
	user, err := u.lookup(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		// Hash anyway, so the response time does not reveal which emails exist.
		_, _ = checkPassword(decoyHash, password)
		return store.User{}, ErrInvalidCredentials
	}
	if err != nil {
		return store.User{}, err
	}

	digest := u.digest(password)
	u.mu.Lock()
	cached, ok := u.verified[email]
	u.mu.Unlock()
	if ok && cached.passwordHash == user.PasswordHash && hmac.Equal(cached.digest, digest) {
		return user, nil
	}

	matches, err := checkPassword(user.PasswordHash, password)
	if err != nil {
		return store.User{}, fmt.Errorf("check password of %s: %w", email, err)
	}
	if !matches {
		return store.User{}, ErrInvalidCredentials
	}
	u.mu.Lock()
	u.verified[email] = verifiedPassword{passwordHash: user.PasswordHash, digest: digest}
	u.mu.Unlock()
	return user, nil
}

func (u *Users) digest(password string) []byte {
	mac := hmac.New(sha256.New, u.cacheKey)
	mac.Write([]byte(password))
	return mac.Sum(nil)
}

// decoyHash has the real cost and matches no password anyone can type: its key
// is all zeros, which PBKDF2 output never is in practice.
var decoyHash = strings.Join([]string{
	hashScheme, strconv.Itoa(hashIterations),
	strings.Repeat("A", 22), strings.Repeat("A", 43), // 16 and 32 zero bytes
}, "$")
