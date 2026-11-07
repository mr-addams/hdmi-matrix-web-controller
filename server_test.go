package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// TestServerEndToEnd walks the whole user path through the assembled server: sign-in through the form,
// the page, the settings API, the matrix proxy and sign-out.
func TestServerEndToEnd(t *testing.T) {
	matrix, _ := startFakeMatrix(t)
	auth := newAuthenticator("ops", "s3cret", false)
	store := &Store{path: filepath.Join(t.TempDir(), "config.json")}
	server := httptest.NewServer(newServerHandler(auth, store, loopbackAllowed, matrix.Client().Transport))
	t.Cleanup(server.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: noRedirects}
	do := func(method, path string, form url.Values, body string) (int, string) {
		var reader io.Reader
		if form != nil {
			reader = strings.NewReader(form.Encode())
		} else if body != "" {
			reader = strings.NewReader(body)
		}
		request, _ := http.NewRequest(method, server.URL+path, reader)
		if form != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}

	if code, _ := do(http.MethodGet, "/", nil, ""); code != http.StatusSeeOther {
		t.Errorf("главная без входа: %d", code)
	}
	if code, _ := do(http.MethodPost, "/login", url.Values{"login": {"ops"}, "password": {"nope"}}, ""); code != http.StatusSeeOther {
		t.Errorf("неверный вход: %d", code)
	}
	if code, _ := do(http.MethodPost, "/login", url.Values{"login": {"ops"}, "password": {"s3cret"}}, ""); code != http.StatusSeeOther {
		t.Fatalf("верный вход: %d", code)
	}
	if code, body := do(http.MethodGet, "/", nil, ""); code != http.StatusOK || !strings.Contains(body, "HDMI Matrix") {
		t.Errorf("главная после входа: %d", code)
	}
	if code, body := do(http.MethodGet, "/device/127.0.0.1:"+portOf(matrix)+"/AutoGetRevID=1", nil, ""); code != http.StatusOK || body != "52=ok" {
		t.Errorf("прокси после входа: %d %q", code, body)
	}
	if code, _ := do(http.MethodPut, "/api/config", nil, `{"version":1,"devices":{}}`); code != http.StatusNoContent {
		t.Errorf("PUT настроек: %d", code)
	}
	if code, body := do(http.MethodGet, "/api/config", nil, ""); code != http.StatusOK || !strings.Contains(body, `"version":1`) {
		t.Errorf("GET настроек: %d %s", code, body)
	}
	if code, _ := do(http.MethodPost, "/logout", nil, ""); code != http.StatusSeeOther {
		t.Errorf("выход: %d", code)
	}
	if code, _ := do(http.MethodGet, "/api/config", nil, ""); code != http.StatusUnauthorized {
		t.Errorf("API после выхода: %d", code)
	}
}
