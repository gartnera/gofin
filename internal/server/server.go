package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

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
	webRoot    string
}

// Option configures a Server.
type Option func(*Server)

// WithScanner injects the scanner used to service library refresh requests,
// allowing it to be shared with a filesystem watcher. When omitted, the server
// constructs its own scanner backed by the same client.
func WithScanner(sc *scanner.Scanner) Option {
	return func(s *Server) { s.scanner = sc }
}

// WithWebRoot enables serving the bundled Jellyfin web client from the given
// filesystem directory at /web/. When set, requests to / are redirected to
// /web/index.html so a browser pointed at the server lands on the client.
func WithWebRoot(dir string) Option {
	return func(s *Server) { s.webRoot = dir }
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
		// /web/ serves the bundled static client whose chunk filenames are
		// case-sensitive (e.g. MaterialIcons-Regular.*); leave those alone.
		if strings.HasPrefix(r.URL.Path, "/web/") {
			next.ServeHTTP(w, r)
			return
		}
		p := strings.ToLower(r.URL.Path)
		// Jellyfin clients append the container as a fake extension on stream
		// URLs (`/videos/{id}/stream.mp4`). Strip it so a single route matches.
		if i := strings.LastIndex(p, "/stream."); i >= 0 {
			p = p[:i+len("/stream")]
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = p
		if r.URL.RawPath != "" {
			r2.URL.RawPath = p
		}
		next.ServeHTTP(w, r2)
	})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corsHandler.Handler(normalizePath(accessLog(s.mux))).ServeHTTP(w, r)
}

// statusRecorder captures the response status so the access log can include it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// accessLog logs every non-static request with status and duration. The web
// client makes a noisy number of asset requests; those are filtered out.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/web/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("%d %s %s (%s)", rec.status, r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	})
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

	// Metadata editing (admin only), mirroring Jellyfin's POST /Items/{itemId}.
	s.mux.HandleFunc("POST /items/{itemId}", s.requireAdmin(s.handleUpdateItem))

	// Items.
	s.mux.HandleFunc("GET /items", s.requireAuth(s.handleItems))
	s.mux.HandleFunc("GET /users/{userId}/items", s.requireAuth(s.handleItems))
	s.mux.HandleFunc("GET /items/{itemId}", s.requireAuth(s.handleItemByID))
	s.mux.HandleFunc("GET /users/{userId}/items/{itemId}", s.requireAuth(s.handleItemByID))
	s.mux.HandleFunc("GET /useritems/resume", s.requireAuth(s.handleResumeItems))
	s.mux.HandleFunc("GET /users/{userId}/items/resume", s.requireAuth(s.handleResumeItems))
	s.mux.HandleFunc("GET /items/latest", s.requireAuth(s.handleLatestItems))
	s.mux.HandleFunc("GET /users/{userId}/items/latest", s.requireAuth(s.handleLatestItems))
	s.mux.HandleFunc("GET /shows/nextup", s.requireAuth(s.handleNextUp))
	s.mux.HandleFunc("GET /shows/{seriesId}/seasons", s.requireAuth(s.handleSeasons))
	s.mux.HandleFunc("GET /shows/{seriesId}/episodes", s.requireAuth(s.handleEpisodes))
	s.mux.HandleFunc("GET /items/{itemId}/similar", s.requireAuth(s.handleSimilar))
	s.mux.HandleFunc("GET /items/{itemId}/thememedia", s.requireAuth(s.handleThemeMedia))
	s.mux.HandleFunc("GET /items/{itemId}/ancestors", s.requireAuth(s.handleItemAncestors))
	s.mux.HandleFunc("GET /livetv/programs", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /livetv/recommendedprograms", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /livetv/channels", s.requireAuth(s.handleEmptyQuery))

	// Library browse facets the web client's per-library tabs request
	// (Suggestions / Genres / Collections / TV Networks / Artists). gofin
	// doesn't index genres, studios, or recommendations, so these return empty
	// results — without them the client 404s and the tab fails to render.
	s.mux.HandleFunc("GET /genres", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /musicgenres", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /studios", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /artists", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /artists/albumartists", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /shows/upcoming", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /movies/recommendations", s.requireAuth(s.handleEmptyArray))
	// Intros and trickplay metadata: gofin has none — return empty results so
	// the player doesn't bail out before it issues PlaybackInfo.
	s.mux.HandleFunc("GET /users/{userId}/items/{itemId}/intros", s.requireAuth(s.handleEmptyQuery))
	s.mux.HandleFunc("GET /items/{itemId}/intros", s.requireAuth(s.handleEmptyQuery))
	// Media segments (intro/outro/credits skip markers): gofin doesn't detect
	// them, so return an empty result rather than 404 — the client just shows
	// no skip buttons.
	s.mux.HandleFunc("GET /mediasegments/{itemId}", s.requireAuth(s.handleEmptyQuery))

	// Playback.
	s.mux.HandleFunc("POST /items/{itemId}/playbackinfo", s.requireAuth(s.handlePlaybackInfo))
	s.mux.HandleFunc("GET /items/{itemId}/playbackinfo", s.requireAuth(s.handlePlaybackInfo))

	// Direct-play streaming. Path normalization strips any trailing container
	// extension (e.g. `stream.mp4` -> `stream`) so a single route handles both.
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
	s.mux.HandleFunc("POST /displaypreferences/{displayPreferencesId}", s.requireAuth(s.handleSetDisplayPreferences))
	s.mux.HandleFunc("GET /system/endpoint", s.requireAuth(s.handleEndpointInfo))
	s.mux.HandleFunc("GET /playback/bitratetest", s.requireAuth(s.handleBitrateTest))
	s.mux.HandleFunc("GET /syncplay/list", s.requireAuth(s.handleSyncPlayList))

	// Bundled web client.
	if s.webRoot != "" {
		fs := http.FileServer(http.Dir(s.webRoot))
		s.mux.Handle("GET /web/", http.StripPrefix("/web/", fs))
		s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/web/", http.StatusFound)
		})
	}
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
