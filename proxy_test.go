package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMatrix изображает матрицу: записывает, что до неё дошло, и отвечает фиксированным телом.
type fakeMatrix struct {
	path, query, authorization, cookie, custom, contentType string
}

func startFakeMatrix(t *testing.T) (*httptest.Server, *fakeMatrix) {
	t.Helper()
	seen := &fakeMatrix{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.path = r.URL.Path
		seen.query = r.URL.RawQuery
		seen.authorization = r.Header.Get("Authorization")
		seen.cookie = r.Header.Get("Cookie")
		seen.custom = r.Header.Get("X-Custom")
		seen.contentType = r.Header.Get("Content-Type")
		fmt.Fprint(w, "52=ok")
	}))
	t.Cleanup(server.Close)
	return server, seen
}

// loopbackAllowed пускает тесты на 127.0.0.1: в реальном коде доступ ограничен приватными сетями.
func loopbackAllowed(ip net.IP) bool { return ip.IsLoopback() }

func portOf(server *httptest.Server) string {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	return port
}

func TestIsPrivateIP(t *testing.T) {
	cases := map[string]bool{
		"10.0.0.1":       true,
		"10.255.255.255": true,
		"172.16.0.1":     true,
		"172.31.255.255": true,
		"172.15.0.1":     false,
		"172.32.0.1":     false,
		"192.168.1.50":   true,
		"192.169.0.1":    false,
		"8.8.8.8":        false,
		"127.0.0.1":      false,
		"::1":            false,
	}
	for raw, want := range cases {
		if got := isPrivateIP(net.ParseIP(raw)); got != want {
			t.Errorf("%s: %v, ожидали %v", raw, got, want)
		}
	}
}

func TestDeviceProxyForwardsPathQueryAndDropsAuthorization(t *testing.T) {
	server, seen := startFakeMatrix(t)
	handler := newDeviceHandler(loopbackAllowed, server.Client().Transport)
	request := httptest.NewRequest(http.MethodGet, "/device/127.0.0.1:"+portOf(server)+"/AutoGetRevID=0.5?x=1", nil)
	request.Header.Set("Authorization", "Basic c2VjcmV0")
	request.Header.Set("Cookie", "hdmi_session=секрет-сессии")
	request.Header.Set("X-Custom", "лишний")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != "52=ok" {
		t.Fatalf("ответ: %d %q", recorder.Code, recorder.Body.String())
	}
	if seen.path != "/AutoGetRevID=0.5" || seen.query != "x=1" {
		t.Errorf("до матрицы дошло %q?%q", seen.path, seen.query)
	}
	if seen.authorization != "" {
		t.Errorf("логин страницы ушёл матрице: %q", seen.authorization)
	}
	// Токен сессии — тот же класс секрета: он не должен уходить устройству в сети.
	if seen.cookie != "" {
		t.Errorf("cookie сессии ушёл матрице: %q", seen.cookie)
	}
	if seen.custom != "" {
		t.Errorf("заголовок вне списка ушёл матрице: %q", seen.custom)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Error("нет Cache-Control: no-store")
	}
}

func TestDeviceProxyRejectsBadTargets(t *testing.T) {
	handler := newDeviceHandler(isPrivateIP, http.DefaultTransport)
	cases := map[string]int{
		"/device/10.0.0.1:0/x":     http.StatusBadRequest,
		"/device/10.0.0.1:65536/x": http.StatusBadRequest,
		"/device/10.0.0.1:99999/x": http.StatusBadRequest,
		"/device/10.0.0.1:080/x":   http.StatusBadRequest,
		"/device/999.1.1.1/x":      http.StatusBadRequest,
		"/device/10.0.0.1":         http.StatusNotFound,
	}
	for path, want := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != want {
			t.Errorf("%s: код %d, ожидали %d", path, recorder.Code, want)
		}
	}
}

func TestDeviceProxyForbidsPublicAddresses(t *testing.T) {
	handler := newDeviceHandler(isPrivateIP, http.DefaultTransport)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/device/8.8.8.8/x", nil))
	if recorder.Code != http.StatusForbidden {
		t.Errorf("публичный адрес: код %d, ожидали 403", recorder.Code)
	}
}

func TestDeviceProxyReportsUnreachableMatrix(t *testing.T) {
	// Порт, который сразу закрыли: соединение получит отказ, а не ждёт таймаута.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	handler := newDeviceHandler(loopbackAllowed, newMatrixTransport())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/device/127.0.0.1:%d/x", port), nil))
	if recorder.Code != http.StatusBadGateway {
		t.Errorf("недоступная матрица: код %d, ожидали 502", recorder.Code)
	}
	body, _ := io.ReadAll(recorder.Body)
	if !strings.Contains(string(body), "не отвечает") {
		t.Errorf("текст ошибки: %q", body)
	}
}
