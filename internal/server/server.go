package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/internal/scanner"
	"github.com/rs/cors"
)

// Version is the reported server version.
const Version = "10.11.10"

// ProductName is reported in system info responses.
const ProductName = "gofin"

// Server holds the dependencies shared by all HTTP handlers.
type Server struct {
	client     *ent.Client
	serverName string
	serverID   string
	scanner    *scanner.Scanner
	mux        *http.ServeMux
}

// Option configures a Server.
type Option func(*Server)

// WithScanner injects the scanner used to service library refresh requests,
// allowing it to be shared with a filesystem watcher. When omitted, the server
// constructs its own scanner backed by the same client.
func WithScanner(sc *scanner.Scanner) Option {
	return func(s *Server) { s.scanner = sc }
}

// New constructs a Server and registers its routes.
func New(client *ent.Client, serverName string, opts ...Option) *Server {
	s := &Server{
		client:     client,
		serverName: serverName,
		serverID:   deriveServerID(serverName),
		mux:        http.NewServeMux(),
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.scanner == nil {
		s.scanner = scanner.New(client)
	}
	s.routes()
	return s
}

// deriveServerID produces a stable 32-char hex id from the server name.
func deriveServerID(name string) string {
	sum := sha256.Sum256([]byte("gofin:" + name))
	return hex.EncodeToString(sum[:16])
}

var corsHandler = cors.New(cors.Options{
	AllowedOrigins: []string{"*"},
	AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
	AllowedHeaders: []string{"Authorization", "Content-Type", "X-Emby-Authorization", "X-MediaBrowser-Token"},
})

// normalizePath lowercases each static segment of the URL path while
// preserving the original path values that will be extracted by ServeMux.
// Jellyfin clients send paths like /users/public; routes are registered in
// title-case (/Users/Public), so we canonicalize before routing.
func normalizePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.ToLower(r.URL.Path)
		if r.URL.RawPath != "" {
			r2.URL.RawPath = strings.ToLower(r.URL.RawPath)
		}
		next.ServeHTTP(w, r2)
	})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corsHandler.Handler(normalizePath(s.mux)).ServeHTTP(w, r)
}

func (s *Server) routes() {
	// Unauthenticated discovery + login.
	s.mux.HandleFunc("GET /system/info/public", s.handlePublicSystemInfo)
	s.mux.HandleFunc("GET /system/ping", s.handlePing)
	s.mux.HandleFunc("POST /system/ping", s.handlePing)
	s.mux.HandleFunc("POST /users/authenticatebyname", s.handleAuthenticateByName)
	s.mux.HandleFunc("GET /branding/configuration", s.handleBrandingConfiguration)
	s.mux.HandleFunc("GET /users/public", s.handlePublicUsers)
	s.mux.HandleFunc("GET /quickconnect/enabled", s.handleQuickConnectEnabled)

	// System / user info.
	s.mux.HandleFunc("GET /system/info", s.requireAuth(s.handleSystemInfo))
	s.mux.HandleFunc("GET /users/me", s.requireAuth(s.handleCurrentUser))
	s.mux.HandleFunc("GET /users", s.requireAuth(s.handleUsers))
	s.mux.HandleFunc("GET /users/{userId}", s.requireAuth(s.handleUserByID))

	// Library views.
	s.mux.HandleFunc("GET /userviews", s.requireAuth(s.handleUserViews))
	s.mux.HandleFunc("GET /users/{userId}/views", s.requireAuth(s.handleUserViews))

	// Library scan / item refresh (admin only).
	s.mux.HandleFunc("POST /library/refresh", s.requireAdmin(s.handleRefreshLibraries))
	s.mux.HandleFunc("POST /items/{itemId}/refresh", s.requireAdmin(s.handleRefreshItem))

	// Items.
	s.mux.HandleFunc("GET /items", s.requireAuth(s.handleItems))
	s.mux.HandleFunc("GET /users/{userId}/items", s.requireAuth(s.handleItems))
	s.mux.HandleFunc("GET /items/{itemId}", s.requireAuth(s.handleItemByID))
	s.mux.HandleFunc("GET /users/{userId}/items/{itemId}", s.requireAuth(s.handleItemByID))
	s.mux.HandleFunc("GET /useritems/resume", s.requireAuth(s.handleResumeItems))
	s.mux.HandleFunc("GET /users/{userId}/items/resume", s.requireAuth(s.handleResumeItems))

	// Playback.
	s.mux.HandleFunc("POST /items/{itemId}/playbackinfo", s.requireAuth(s.handlePlaybackInfo))
	s.mux.HandleFunc("GET /items/{itemId}/playbackinfo", s.requireAuth(s.handlePlaybackInfo))

	// Direct-play streaming.
	s.mux.HandleFunc("GET /videos/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("HEAD /videos/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("GET /audio/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("HEAD /audio/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("GET /audio/{itemId}/universal", s.requireAuth(s.handleStream))

	// Images (unauthenticated, as Jellyfin serves them).
	s.mux.HandleFunc("GET /items/{itemId}/images/{imageType}", s.handleImage)

	// Playback reporting + play state.
	s.mux.HandleFunc("POST /sessions/playing", s.requireAuth(s.handlePlaybackStart))
	s.mux.HandleFunc("POST /sessions/playing/progress", s.requireAuth(s.handlePlaybackProgress))
	s.mux.HandleFunc("POST /sessions/playing/stopped", s.requireAuth(s.handlePlaybackStopped))
	s.mux.HandleFunc("POST /sessions/playing/ping", s.requireAuth(s.handleNoContent))
	s.mux.HandleFunc("POST /playingitems/{itemId}", s.requireAuth(s.handlePlaybackStart))
	s.mux.HandleFunc("POST /playingitems/{itemId}/progress", s.requireAuth(s.handlePlaybackProgress))
	s.mux.HandleFunc("DELETE /playingitems/{itemId}", s.requireAuth(s.handlePlaybackStopped))
	s.mux.HandleFunc("POST /userplayeditems/{itemId}", s.requireAuth(s.handleMarkPlayed))
	s.mux.HandleFunc("DELETE /userplayeditems/{itemId}", s.requireAuth(s.handleMarkUnplayed))

	// Session / client niceties.
	s.mux.HandleFunc("POST /sessions/capabilities", s.requireAuth(s.handleNoContent))
	s.mux.HandleFunc("POST /sessions/capabilities/full", s.requireAuth(s.handleNoContent))
	s.mux.HandleFunc("POST /sessions/logout", s.requireAuth(s.handleLogout))
	s.mux.HandleFunc("GET /displaypreferences/{displayPreferencesId}", s.requireAuth(s.handleDisplayPreferences))
}

// writeJSON serialises v as JSON, relying on the jellyfin-go models'
// MarshalJSON to produce Jellyfin-compatible field naming.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ProductName)
}

func (s *Server) handleNoContent(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
