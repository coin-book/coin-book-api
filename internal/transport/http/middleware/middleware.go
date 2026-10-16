package middleware

import (
	"net"
	"net/http"
	"slices"
	"strings"

	"coin-book-api/internal/transport/http/apierr"
)

// RequireAllowedHost refuses requests that name an unexpected Host, which is what
// a DNS-rebinding page in the browser would send to a localhost service.
func RequireAllowedHost(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if !slices.Contains(allowed, strings.ToLower(host)) {
				apierr.Write(w, apierr.Forbidden(apierr.CodeHostNotAllowed))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
