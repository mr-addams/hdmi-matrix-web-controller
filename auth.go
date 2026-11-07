package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "hdmi_session"
	sessionTTL    = 12 * time.Hour
	// A failed sign-in waits half a second. This does not stop brute force; it only slows it down.
	failedLoginDelay = 500 * time.Millisecond
)

// loginPattern uses the same login rules as install.sh: the login ends up in .env, so
// colons, spaces and control characters are not allowed.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// validateCredentials checks the login and password at startup: it is better to fail at once than to accept
// an empty password.
func validateCredentials(login, password string) error {
	if !loginPattern.MatchString(login) {
		return errors.New("UI_USER: только латиница, цифры, точка, подчёркивание и дефис, до 32 знаков")
	}
	if password == "" {
		return errors.New("UI_PASSWORD не задан")
	}
	if strings.ContainsAny(password, "\r\n") {
		return errors.New("UI_PASSWORD не должен содержать перевод строки")
	}
	return nil
}

// credentials keeps hashes of the login and password. Comparing fixed-length hashes keeps the response time
// from revealing the real password length. Both comparisons always run.
type credentials struct {
	login    [sha256.Size]byte
	password [sha256.Size]byte
}

func newCredentials(login, password string) credentials {
	return credentials{login: sha256.Sum256([]byte(login)), password: sha256.Sum256([]byte(password))}
}

func (c credentials) match(login, password string) bool {
	gotLogin := sha256.Sum256([]byte(login))
	gotPassword := sha256.Sum256([]byte(password))
	loginMatch := subtle.ConstantTimeCompare(gotLogin[:], c.login[:])
	passwordMatch := subtle.ConstantTimeCompare(gotPassword[:], c.password[:])
	return loginMatch&passwordMatch == 1
}

// sessionStore keeps issued sessions in memory. A service restart drops all sessions,
// which is intentional: on a local install signing in again is a small price for having no database.
type sessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	expiries map[string]time.Time
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{ttl: ttl, now: time.Now, expiries: map[string]time.Time{}}
}

// issue creates a token from 32 random bytes, so it cannot be guessed. It also removes expired sessions.
func (s *sessionStore) issue() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for stale, expiry := range s.expiries {
		if !now.Before(expiry) {
			delete(s.expiries, stale)
		}
	}
	s.expiries[token] = now.Add(s.ttl)
	return token, nil
}

func (s *sessionStore) valid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.expiries[token]
	if !ok {
		return false
	}
	if !s.now().Before(expiry) {
		delete(s.expiries, token)
		return false
	}
	return true
}

func (s *sessionStore) revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.expiries, token)
}

// authenticator ties together sign-in, sign-out and session checks.
type authenticator struct {
	creds        credentials
	sessions     *sessionStore
	secureCookie bool
}

func newAuthenticator(login, password string, secureCookie bool) *authenticator {
	return &authenticator{
		creds:        newCredentials(login, password),
		sessions:     newSessionStore(sessionTTL),
		secureCookie: secureCookie,
	}
}

func (a *authenticator) signedIn(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	return err == nil && a.sessions.valid(cookie.Value)
}

// loginPage serves the sign-in page. A user who is already signed in goes to the main page.
func (a *authenticator) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.signedIn(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	page, err := fs.ReadFile(staticFiles, "static/login.html")
	if err != nil {
		http.Error(w, "страница входа недоступна", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// loginSubmit checks the login and password from the form and issues the session cookie.
func (a *authenticator) loginSubmit(w http.ResponseWriter, r *http.Request) {
	// The sign-in form is short: the limit stops an unauthenticated request from sending a large body.
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "некорректная форма", http.StatusBadRequest)
		return
	}
	if !a.creds.match(r.PostForm.Get("login"), r.PostForm.Get("password")) {
		time.Sleep(failedLoginDelay)
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	token, err := a.sessions.issue()
	if err != nil {
		log.Printf("выдача сессии: %v", err)
		http.Error(w, "не удалось создать сессию", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.cookie(token, int(sessionTTL.Seconds())))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *authenticator) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		a.sessions.revoke(cookie.Value)
	}
	http.SetCookie(w, a.cookie("", -1))
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// cookie: HttpOnly keeps page scripts from reading it; SameSite=Strict stops other sites from
// sending requests with our session. Secure is enabled when the service sits behind an HTTPS proxy:
// over plain HTTP, browsers do not store such a cookie.
func (a *authenticator) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   a.secureCookie,
	}
}

// require lets a request through only with a session. Pages redirect to sign-in; the API and the proxy answer 401,
// because page scripts need an error code, not sign-in HTML instead of JSON.
func (a *authenticator) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.signedIn(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/device/") {
			http.Error(w, "нужен вход", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// securityHeaders sets headers on every response, including sign-in failures.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
