package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/gartnera/gofin/ent"
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
	mux        *http.ServeMux
}

// New constructs a Server and registers its routes.
func New(client *ent.Client, serverName string) *Server {
	s := &Server{
		client:     client,
		serverName: serverName,
		serverID:   deriveServerID(serverName),
		mux:        http.NewServeMux(),
	}
	s.routes()
	return s
}

// deriveServerID produces a stable 32-char hex id from the server name.
func deriveServerID(name string) string {
	sum := sha256.Sum256([]byte("gofin:" + name))
	return hex.EncodeToString(sum[:16])
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// Unauthenticated discovery + login.
	s.mux.HandleFunc("GET /System/Info/Public", s.handlePublicSystemInfo)
	s.mux.HandleFunc("GET /System/Ping", s.handlePing)
	s.mux.HandleFunc("POST /System/Ping", s.handlePing)
	s.mux.HandleFunc("POST /Users/AuthenticateByName", s.handleAuthenticateByName)
	s.mux.HandleFunc("GET /Branding/Configuration", s.handleBrandingConfiguration)
	s.mux.HandleFunc("GET /Users/Public", s.handlePublicUsers)
	s.mux.HandleFunc("GET /QuickConnect/Enabled", s.handleQuickConnectEnabled)

	// System / user info.
	s.mux.HandleFunc("GET /System/Info", s.requireAuth(s.handleSystemInfo))
	s.mux.HandleFunc("GET /Users/Me", s.requireAuth(s.handleCurrentUser))
	s.mux.HandleFunc("GET /Users", s.requireAuth(s.handleUsers))
	s.mux.HandleFunc("GET /Users/{userId}", s.requireAuth(s.handleUserByID))

	// Library views.
	s.mux.HandleFunc("GET /UserViews", s.requireAuth(s.handleUserViews))
	s.mux.HandleFunc("GET /Users/{userId}/Views", s.requireAuth(s.handleUserViews))

	// Items.
	s.mux.HandleFunc("GET /Items", s.requireAuth(s.handleItems))
	s.mux.HandleFunc("GET /Users/{userId}/Items", s.requireAuth(s.handleItems))
	s.mux.HandleFunc("GET /Items/{itemId}", s.requireAuth(s.handleItemByID))
	s.mux.HandleFunc("GET /Users/{userId}/Items/{itemId}", s.requireAuth(s.handleItemByID))
	s.mux.HandleFunc("GET /UserItems/Resume", s.requireAuth(s.handleResumeItems))
	s.mux.HandleFunc("GET /Users/{userId}/Items/Resume", s.requireAuth(s.handleResumeItems))

	// Playback.
	s.mux.HandleFunc("POST /Items/{itemId}/PlaybackInfo", s.requireAuth(s.handlePlaybackInfo))
	s.mux.HandleFunc("GET /Items/{itemId}/PlaybackInfo", s.requireAuth(s.handlePlaybackInfo))

	// Direct-play streaming.
	s.mux.HandleFunc("GET /Videos/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("HEAD /Videos/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("GET /Audio/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("HEAD /Audio/{itemId}/stream", s.requireAuth(s.handleStream))
	s.mux.HandleFunc("GET /Audio/{itemId}/universal", s.requireAuth(s.handleStream))

	// Images (unauthenticated, as Jellyfin serves them).
	s.mux.HandleFunc("GET /Items/{itemId}/Images/{imageType}", s.handleImage)

	// Playback reporting + play state.
	s.mux.HandleFunc("POST /Sessions/Playing", s.requireAuth(s.handlePlaybackStart))
	s.mux.HandleFunc("POST /Sessions/Playing/Progress", s.requireAuth(s.handlePlaybackProgress))
	s.mux.HandleFunc("POST /Sessions/Playing/Stopped", s.requireAuth(s.handlePlaybackStopped))
	s.mux.HandleFunc("POST /Sessions/Playing/Ping", s.requireAuth(s.handleNoContent))
	s.mux.HandleFunc("POST /PlayingItems/{itemId}", s.requireAuth(s.handlePlaybackStart))
	s.mux.HandleFunc("POST /PlayingItems/{itemId}/Progress", s.requireAuth(s.handlePlaybackProgress))
	s.mux.HandleFunc("DELETE /PlayingItems/{itemId}", s.requireAuth(s.handlePlaybackStopped))
	s.mux.HandleFunc("POST /UserPlayedItems/{itemId}", s.requireAuth(s.handleMarkPlayed))
	s.mux.HandleFunc("DELETE /UserPlayedItems/{itemId}", s.requireAuth(s.handleMarkUnplayed))

	// Session / client niceties.
	s.mux.HandleFunc("POST /Sessions/Capabilities", s.requireAuth(s.handleNoContent))
	s.mux.HandleFunc("POST /Sessions/Capabilities/Full", s.requireAuth(s.handleNoContent))
	s.mux.HandleFunc("POST /Sessions/Logout", s.requireAuth(s.handleLogout))
	s.mux.HandleFunc("GET /DisplayPreferences/{displayPreferencesId}", s.requireAuth(s.handleDisplayPreferences))
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
