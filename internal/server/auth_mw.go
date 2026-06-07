package server

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/accesstoken"
)

type ctxKey int

const userCtxKey ctxKey = iota

// authPairRe matches key="value" pairs in a MediaBrowser authorization header.
var authPairRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// ParseAuthorization parses a MediaBrowser/Emby authorization header value into
// its key/value pairs (Token, Client, Device, DeviceId, Version).
func ParseAuthorization(header string) map[string]string {
	out := map[string]string{}
	if header == "" {
		return out
	}
	// Strip the scheme prefix (e.g. "MediaBrowser " or "Emby ") if present.
	if i := strings.IndexByte(header, ' '); i >= 0 {
		if scheme := header[:i]; scheme == "MediaBrowser" || scheme == "Emby" {
			header = header[i+1:]
		}
	}
	for _, m := range authPairRe.FindAllStringSubmatch(header, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// tokenFromRequest extracts an access token from the request, checking the
// Authorization and X-Emby-Authorization headers, the X-Emby-Token /
// X-MediaBrowser-Token headers, and the ApiKey/api_key query params.
func tokenFromRequest(r *http.Request) string {
	for _, h := range []string{r.Header.Get("Authorization"), r.Header.Get("X-Emby-Authorization")} {
		if pairs := ParseAuthorization(h); pairs["Token"] != "" {
			return pairs["Token"]
		}
	}
	if t := r.Header.Get("X-Emby-Token"); t != "" {
		return t
	}
	if t := r.Header.Get("X-MediaBrowser-Token"); t != "" {
		return t
	}
	q := r.URL.Query()
	if t := q.Get("ApiKey"); t != "" {
		return t
	}
	if t := q.Get("api_key"); t != "" {
		return t
	}
	return ""
}

// userFrom returns the authenticated user stored in the request context.
func userFrom(ctx context.Context) *ent.User {
	u, _ := ctx.Value(userCtxKey).(*ent.User)
	return u
}

// authenticate resolves the request's token to a user, or returns nil.
func (s *Server) authenticate(r *http.Request) *ent.User {
	token := tokenFromRequest(r)
	if token == "" {
		return nil
	}
	u, err := s.client.AccessToken.Query().
		Where(accesstoken.TokenEQ(token)).
		QueryUser().
		Only(r.Context())
	if err != nil {
		return nil
	}
	return u
}

// requireAuth wraps a handler, rejecting requests without a valid token.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.authenticate(r)
		if u == nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		h(w, r.WithContext(ctx))
	}
}
