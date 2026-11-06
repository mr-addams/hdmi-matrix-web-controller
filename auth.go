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
	// Неудачный вход ждёт полсекунды. Это не защита от перебора, а способ сделать его медленным.
	failedLoginDelay = 500 * time.Millisecond
)

// loginPattern — правила логина те же, что в install.sh: логин попадает в .env, поэтому
// двоеточие, пробелы и управляющие символы запрещены.
var loginPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// validateCredentials проверяет логин и пароль при старте: лучше упасть сразу, чем принять
// пустой пароль.
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

// credentials хранит хеши логина и пароля. Сравниваем хеши фиксированной длины: так время
// ответа не выдаёт длину настоящего пароля. Оба сравнения выполняются всегда.
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

// sessionStore держит выданные входы в памяти. После перезапуска сервиса все входы теряются,
// и это осознанно: на локальной установке повторный вход — небольшая цена за отсутствие базы.
type sessionStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	expiries map[string]time.Time
}

func newSessionStore(ttl time.Duration) *sessionStore {
	return &sessionStore{ttl: ttl, now: time.Now, expiries: map[string]time.Time{}}
}

// issue создаёт токен из 32 случайных байт: угадать его нельзя. Заодно убирает истёкшие.
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

// authenticator связывает вход, выход и проверку сессии.
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

// loginPage отдаёт страницу входа. Уже вошедшего отправляем на главную.
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

// loginSubmit проверяет логин и пароль из формы и выдаёт cookie сессии.
func (a *authenticator) loginSubmit(w http.ResponseWriter, r *http.Request) {
	// Форма входа короткая: лимит не даёт неаутентифицированному запросу прислать большое тело.
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

// cookie: HttpOnly — скрипт страницы его не читает; SameSite=Strict — чужой сайт не может
// отправить запрос с нашей сессией. Secure включается, когда сервис стоит за HTTPS-прокси:
// по голому HTTP браузер такой cookie не сохранит.
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

// require пускает дальше только с сессией. Страницы уходят на вход, а API и прокси отвечают 401:
// скрипту страницы нужен код ошибки, а не HTML входа вместо JSON.
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

// securityHeaders ставит заголовки на все ответы, включая отказы входа.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
