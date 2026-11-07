package main

import (
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// devicePattern parses /device/<ip>[:<port>]/<path>. The port is optional and defaults to 80.
var devicePattern = regexp.MustCompile(`^/device/(\d{1,3}(?:\.\d{1,3}){3})(?::(\d{1,5}))?(/.*)$`)

// privateNets are the private networks the proxy will reach at all. The proxy takes the address from the path,
// without this limit the container would become an open proxy to any network address.
var privateNets = []*net.IPNet{
	mustCIDR("10.0.0.0/8"),
	mustCIDR("172.16.0.0/12"),
	mustCIDR("192.168.0.0/16"),
}

func mustCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}

// isPrivateIP reports whether an address is on a private network. The proxy does not serve public or IPv6 addresses.
func isPrivateIP(ip net.IP) bool {
	for _, network := range privateNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// newMatrixTransport sets timeouts for how the page behaves. A connection to an empty address must fail fast:
// network discovery probes many addresses, and a slow failure would slow down
// the whole scan. The response-header limit means a silent matrix holds a request for at most 5 seconds.
func newMatrixTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: time.Second}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		ResponseHeaderTimeout: 5 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
}

// matrixHeaders lists the client headers that are forwarded to a matrix. It is an allowlist, not a blocklist: new
// browser headers never reach the device by default. The session cookie and the page login
// stay on the server and in the browser.
var matrixHeaders = []string{"Content-Type", "Accept"}

func forwardedHeaders(in http.Header) http.Header {
	out := make(http.Header, len(matrixHeaders))
	for _, name := range matrixHeaders {
		if values := in.Values(name); len(values) > 0 {
			out[name] = values
		}
	}
	return out
}

// newDeviceHandler proxies a request to a matrix. It never retries: ReverseProxy does not retry, and a
// retried switch command could switch an output a second time.
func newDeviceHandler(allowed func(net.IP) bool, transport http.RoundTripper) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		match := devicePattern.FindStringSubmatch(r.URL.Path)
		if match == nil {
			http.NotFound(w, r)
			return
		}
		ip := net.ParseIP(match[1])
		if ip == nil {
			http.Error(w, "некорректный адрес", http.StatusBadRequest)
			return
		}
		if !allowed(ip) {
			http.Error(w, "адрес вне приватных сетей", http.StatusForbidden)
			return
		}
		port := 80
		if match[2] != "" {
			// Leading zeros are rejected: "080" and "80" name the same port, and we do not want two spellings of one path.
			if len(match[2]) > 1 && match[2][0] == '0' {
				http.Error(w, "порт с ведущим нулём", http.StatusBadRequest)
				return
			}
			parsed, err := strconv.Atoi(match[2])
			if err != nil || parsed < 1 || parsed > 65535 {
				http.Error(w, "порт вне диапазона 1–65535", http.StatusBadRequest)
				return
			}
			port = parsed
		}

		target := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL = &url.URL{Scheme: "http", Host: target, Path: match[3], RawQuery: pr.In.URL.RawQuery}
				pr.Out.Host = target
				pr.Out.Header = forwardedHeaders(pr.In.Header)
			},
			Transport: transport,
			// No buffering: matrix responses reach the client at once, like proxy_buffering off.
			FlushInterval: -1,
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				log.Printf("матрица %s не ответила: %v", target, err)
				http.Error(w, "матрица не отвечает", http.StatusBadGateway)
			},
		}
		w.Header().Set("Cache-Control", "no-store")
		proxy.ServeHTTP(w, r)
	})
}
