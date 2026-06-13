package server

import (
	"net/http"
	"strconv"

	"github.com/gartnera/gofin/ent/accesstoken"
	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/sj14/jellyfin-go/api"
)

// handlePublicUsers lists users for a login screen (no auth required).
func (s *Server) handlePublicUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.client.User.Query().All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dtos := make([]api.UserDto, 0, len(users))
	for _, u := range users {
		dtos = append(dtos, jellyfin.MapUser(u, s.serverID))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// handleDisplayPreferences returns a minimal, valid display-preferences object
// that web clients fetch on startup.
func (s *Server) handleDisplayPreferences(w http.ResponseWriter, r *http.Request) {
	dp := api.NewDisplayPreferencesDto()
	dp.SetId(r.PathValue("displayPreferencesId"))
	dp.SetCustomPrefs(map[string]string{})
	writeJSON(w, http.StatusOK, dp)
}

// handleSetDisplayPreferences accepts a write of display prefs and discards it.
// The web client posts on every settings change; without a 204 it surfaces a
// generic error and prevents further navigation.
func (s *Server) handleSetDisplayPreferences(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// handleEndpointInfo tells the client whether it's on the local network. We
// always claim yes — gofin doesn't differentiate.
func (s *Server) handleEndpointInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"IsLocal": true, "IsInNetwork": true})
}

// handleBitrateTest streams `size` bytes of zeros so the client can estimate
// throughput and decide direct-play is fine.
func (s *Server) handleBitrateTest(w http.ResponseWriter, r *http.Request) {
	size := atoiDefault(firstNonEmptyQuery(r.URL.Query(), "size", "Size"), 500000)
	if size < 0 {
		size = 0
	}
	if size > 10*1024*1024 {
		size = 10 * 1024 * 1024
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	w.WriteHeader(http.StatusOK)
	buf := make([]byte, 4096)
	for size > 0 {
		n := len(buf)
		if n > size {
			n = size
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		size -= n
	}
}

// handleItemAncestors would return an item's parent chain (used by the web
// client's detail page for breadcrumbs). gofin doesn't surface a breadcrumb
// trail, so return an empty list rather than a 404.
func (s *Server) handleItemAncestors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []api.BaseItemDto{})
}

// handleSyncPlayList reports no active SyncPlay groups. gofin doesn't implement
// SyncPlay; without this the client logs a 404 on startup.
func (s *Server) handleSyncPlayList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

// handleEmptyArray responds with an empty JSON array — used for endpoints whose
// Jellyfin contract is a bare array (not a QueryResult), such as
// /Movies/Recommendations. The web client's library "Suggestions" tab calls it
// and crashes on a 404; an empty array renders an (empty) suggestions view.
func (s *Server) handleEmptyArray(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

// handleLogout revokes the access token presented on the request.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := tokenFromRequest(r); token != "" {
		_, _ = s.client.AccessToken.Delete().Where(accesstoken.TokenEQ(token)).Exec(r.Context())
	}
	w.WriteHeader(http.StatusNoContent)
}
