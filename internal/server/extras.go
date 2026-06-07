package server

import (
	"net/http"

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

// handleQuickConnectEnabled reports that QuickConnect is unavailable.
func (s *Server) handleQuickConnectEnabled(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, false)
}

// handleDisplayPreferences returns a minimal, valid display-preferences object
// that web clients fetch on startup.
func (s *Server) handleDisplayPreferences(w http.ResponseWriter, r *http.Request) {
	dp := api.NewDisplayPreferencesDto()
	dp.SetId(r.PathValue("displayPreferencesId"))
	dp.SetCustomPrefs(map[string]string{})
	writeJSON(w, http.StatusOK, dp)
}

// handleLogout revokes the access token presented on the request.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := tokenFromRequest(r); token != "" {
		_, _ = s.client.AccessToken.Delete().Where(accesstoken.TokenEQ(token)).Exec(r.Context())
	}
	w.WriteHeader(http.StatusNoContent)
}
