package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestValidateCredentials(t *testing.T) {
	good := [][2]string{{"admin", "admin"}, {"ops.user_1-x", "pw"}, {strings.Repeat("a", 32), "pw"}}
	for _, c := range good {
		if err := validateCredentials(c[0], c[1]); err != nil {
			t.Errorf("%q/%q отклонены: %v", c[0], c[1], err)
		}
	}
	bad := [][2]string{{"", "pw"}, {"ad min", "pw"}, {"a:b", "pw"}, {strings.Repeat("a", 33), "pw"}, {"admin", ""}, {"admin", "a\nb"}}
	for _, c := range bad {
		if err := validateCredentials(c[0], c[1]); err == nil {
			t.Errorf("%q/%q приняты", c[0], c[1])
		}
	}
}

func TestCredentialsMatch(t *testing.T) {
	creds := newCredentials("ops", "s3cret")
	if !creds.match("ops", "s3cret") {
		t.Error("верные данные не совпали")
	}
	for _, c := range [][2]string{{"ops", "s3cre"}, {"admin", "s3cret"}, {"ops", ""}, {"", ""}} {
		if creds.match(c[0], c[1]) {
			t.Errorf("%q/%q совпали", c[0], c[1])
		}
	}
}

func TestSessionsExpireAndRevoke(t *testing.T) {
	store := newSessionStore(time.Hour)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }

	token, err := store.issue()
	if err != nil {
		t.Fatal(err)
	}
	if !store.valid(token) {
		t.Fatal("свежая сессия не действует")
	}
	clock = clock.Add(time.Hour)
	if store.valid(token) {
		t.Error("сессия действует после истечения срока")
	}

	token, _ = store.issue()
	store.revoke(token)
	if store.valid(token) {
		t.Error("отозванная сессия действует")
	}
	if store.valid("не-токен") {
		t.Error("случайная строка принята за сессию")
	}
}

func TestSessionTokensAreUniqueAndLongEnough(t *testing.T) {
	store := newSessionStore(time.Hour)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		token, err := store.issue()
		if err != nil {
			t.Fatal(err)
		}
		if len(token) < 43 {
			t.Fatalf("токен короче 32 байт в base64: %d", len(token))
		}
		if seen[token] {
			t.Fatal("повтор токена")
		}
		seen[token] = true
	}
}

// noRedirects нужен, чтобы видеть ответы входа, а не то, куда они ведут.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func TestLoginFlowSetsCookieAttributes(t *testing.T) {
	auth := newAuthenticator("ops", "s3cret", false)
	server := httptest.NewServer(auth.loginSubmitHandler())
	t.Cleanup(server.Close)
	client := &http.Client{CheckRedirect: noRedirects}

	wrong, err := client.PostForm(server.URL+"/login", url.Values{"login": {"ops"}, "password": {"nope"}})
	if err != nil {
		t.Fatal(err)
	}
	wrong.Body.Close()
	if wrong.Header.Get("Location") != "/login?error=1" || len(wrong.Cookies()) != 0 {
		t.Errorf("неверный пароль: Location=%q, cookie=%d", wrong.Header.Get("Location"), len(wrong.Cookies()))
	}

	right, err := client.PostForm(server.URL+"/login", url.Values{"login": {"ops"}, "password": {"s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	right.Body.Close()
	if right.Header.Get("Location") != "/" {
		t.Errorf("верный пароль: Location=%q", right.Header.Get("Location"))
	}
	cookies := right.Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie {
		t.Fatalf("cookie сессии не выдан: %+v", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Errorf("флаги cookie: HttpOnly=%v SameSite=%v Path=%q", cookies[0].HttpOnly, cookies[0].SameSite, cookies[0].Path)
	}
	if cookies[0].Secure {
		t.Error("Secure включён без COOKIE_SECURE")
	}
}

func TestSecureCookieFlagFollowsConfig(t *testing.T) {
	auth := newAuthenticator("ops", "s3cret", true)
	if !auth.cookie("x", 60).Secure {
		t.Error("COOKIE_SECURE=true не ставит Secure")
	}
}

func (a *authenticator) loginSubmitHandler() http.Handler {
	return http.HandlerFunc(a.loginSubmit)
}

func TestRequireRedirectsPagesAndRejectsAPI(t *testing.T) {
	auth := newAuthenticator("ops", "s3cret", false)
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := auth.require(inner)

	cases := map[string]int{
		"/":                               http.StatusSeeOther,
		"/app.js":                         http.StatusSeeOther,
		"/api/config":                     http.StatusUnauthorized,
		"/device/10.0.0.1/AutoGetRevID=1": http.StatusUnauthorized,
	}
	for path, want := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != want {
			t.Errorf("%s без входа: %d, ожидали %d", path, recorder.Code, want)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Header().Get("Location") != "/login" {
		t.Errorf("страница без входа ведёт на %q", recorder.Header().Get("Location"))
	}

	token, _ := auth.sessions.issue()
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("API с сессией: %d", recorder.Code)
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	auth := newAuthenticator("ops", "s3cret", false)
	token, _ := auth.sessions.issue()
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	recorder := httptest.NewRecorder()
	auth.logout(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Errorf("выход: код %d, Location %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if auth.sessions.valid(token) {
		t.Error("после выхода сессия действует")
	}
	setCookie := recorder.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "Max-Age=0") || !strings.Contains(setCookie, "HttpOnly") {
		t.Errorf("Set-Cookie при выходе: %q", setCookie)
	}
}

func TestLoginPageRedirectsSignedInUsers(t *testing.T) {
	auth := newAuthenticator("ops", "s3cret", false)
	recorder := httptest.NewRecorder()
	auth.loginPage(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Sign in") {
		t.Errorf("страница входа: %d", recorder.Code)
	}

	token, _ := auth.sessions.issue()
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	recorder = httptest.NewRecorder()
	auth.loginPage(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/" {
		t.Errorf("уже вошедший: код %d, Location %q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestSecurityHeadersAreSet(t *testing.T) {
	recorder := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("нет nosniff")
	}
}
