package api

import (
	"net/http"
	"strings"
)

// Guard adds what every response needs on top of the token check:
//
//   - Mutating requests need the header "X-Boxhand: 1", like on Boxhand itself.
//   - Responses must not be sniffed, framed or leak a referrer.
type Guard struct{}

// Wrap applies the guard to next.
func (Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("X-Boxhand") != "1" {
				writeErr(w, http.StatusForbidden, "csrf", "header X-Boxhand missing")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
