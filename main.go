// hdmi-ui: страница управления HDMI-матрицами одним бинарником. Отдаёт статику, проксирует
// запросы к матрицам в приватных сетях и хранит подписи и теги. Вход — форма и сессия в cookie.
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed static
var staticFiles embed.FS

func main() {
	login := envOr("UI_USER", "admin")
	password := envOr("UI_PASSWORD", "admin")
	if err := validateCredentials(login, password); err != nil {
		log.Fatalf("hdmi-ui: %v", err)
	}
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	configPath := envOr("CONFIG_PATH", "/data/config.json")

	auth := newAuthenticator(login, password, envOr("COOKIE_SECURE", "") == "true")
	handler := newServerHandler(auth, &Store{path: configPath}, isPrivateIP, newMatrixTransport())

	// Таймауты намеренно заданы константами, а не через ENV: клиент — одна страница, и менять их
	// оператору незачем. WriteTimeout покрывает проксирование к матрице с её ожиданием ответа.
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ListenAndServe возвращает ErrServerClosed сразу, как только Shutdown начал работу. Поэтому
	// ждём явный сигнал о завершении Shutdown: иначе процесс оборвал бы запросы в полёте.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("остановка: %v", err)
		}
	}()

	if envOr("COOKIE_SECURE", "") != "true" {
		log.Printf("COOKIE_SECURE не задан: cookie входа не помечен Secure. За HTTPS-прокси задайте COOKIE_SECURE=true")
	}
	log.Printf("hdmi-ui слушает %s, файл настроек %s", listenAddr, configPath)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("hdmi-ui: %v", err)
	}
	<-shutdownDone
}

// newServerHandler собирает весь сервер. Вход и выход доступны без сессии, всё остальное
// пускает только require. Тесты собирают тем же вызовом, что и продакшен.
func newServerHandler(auth *authenticator, store *Store, allowed func(net.IP) bool, transport http.RoundTripper) http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /login", auth.loginPage)
	root.HandleFunc("POST /login", auth.loginSubmit)
	root.HandleFunc("POST /logout", auth.logout)
	root.Handle("/", auth.require(newMux(store, allowed, transport)))
	return securityHeaders(root)
}

// newMux собирает маршруты приложения без входа: его навешивает newServerHandler.
func newMux(store *Store, allowed func(net.IP) bool, transport http.RoundTripper) http.Handler {
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/config", newHandler(store))
	mux.Handle("/device/", newDeviceHandler(allowed, transport))
	mux.Handle("/", http.FileServerFS(static))
	return mux
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
