// hdmi-ui: web page for controlling HDMI matrices, shipped as one binary. It serves static files, proxies
// requests to matrices on private networks, and stores labels and tags. Sign-in uses a form and a cookie session.
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

	// Timeouts are constants on purpose, not environment variables: the only client is one page, so there is no need to tune them.
	// WriteTimeout covers proxied requests, which may wait for a matrix to answer.
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ListenAndServe returns ErrServerClosed as soon as Shutdown starts, so
	// wait for an explicit signal that Shutdown has finished; otherwise in-flight requests would be cut off.
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

// newServerHandler assembles the whole server. Sign-in and sign-out work without a session; everything else
// goes through require. Tests use this same function, so they exercise the production wiring.
func newServerHandler(auth *authenticator, store *Store, allowed func(net.IP) bool, transport http.RoundTripper) http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /login", auth.loginPage)
	root.HandleFunc("POST /login", auth.loginSubmit)
	root.HandleFunc("POST /logout", auth.logout)
	root.Handle("/", auth.require(newMux(store, allowed, transport)))
	return securityHeaders(root)
}

// newMux builds the application routes without sign-in; newServerHandler wraps them with it.
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
