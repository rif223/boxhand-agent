package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// TokenKey returns the value for Server.TokenKey: the SHA-256 of the agent token in hex.
func TokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// principal is who sent a request (always the agent token; recorded in the activity log).
type principal struct {
	Name string
}

type principalKey struct{}

func principalOf(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey{}).(*principal)
	return p
}

func (s *Server) tokenOK(r *http.Request) bool {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || s.TokenKey == "" {
		return false
	}
	got := TokenKey(strings.TrimSpace(secret))
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.TokenKey)) == 1
}

// withToken lets only requests with the agent token through.
func (s *Server) withToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(r) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "agent token missing or wrong")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, &principal{Name: "agent"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
