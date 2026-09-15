package server

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RequestHeader must be sent on every state-changing API request. Browsers
// cannot attach custom headers cross-origin without a CORS preflight, and
// the server never grants CORS, so this defeats CSRF without tokens.
const RequestHeader = "X-Requested-With"

// RequestHeaderValue is the expected value of RequestHeader.
const RequestHeaderValue = "ostiole"

// csrfGuard rejects cross-site state-changing API requests. It layers the
// custom header requirement with Origin and Sec-Fetch-Site checks.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !safeMethod(r.Method) {
			if r.Header.Get(RequestHeader) != RequestHeaderValue {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing " + RequestHeader + " header"})
				return
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin mismatch"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// securityHeaders sets a strict baseline. The UI ships no third-party assets,
// so a self-only CSP is enough. Styles allow inline because Vue and Tailwind
// emit inline style attributes at runtime.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; font-src 'self'; connect-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
			"remote", r.RemoteAddr,
		)
	})
}
