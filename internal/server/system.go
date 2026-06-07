package server

import (
	"net/http"

	"github.com/sj14/jellyfin-go/api"
)

func (s *Server) handlePublicSystemInfo(w http.ResponseWriter, r *http.Request) {
	info := api.NewPublicSystemInfo()
	info.SetId(s.serverID)
	info.SetServerName(s.serverName)
	info.SetVersion(Version)
	info.SetProductName(ProductName)
	info.SetStartupWizardCompleted(true)
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	info := api.NewSystemInfo()
	info.SetId(s.serverID)
	info.SetServerName(s.serverName)
	info.SetVersion(Version)
	info.SetProductName(ProductName)
	info.SetOperatingSystem("linux")
	info.SetStartupWizardCompleted(true)
	writeJSON(w, http.StatusOK, info)
}

// handleBrandingConfiguration returns a minimal valid branding object that web
// clients poll on startup.
func (s *Server) handleBrandingConfiguration(w http.ResponseWriter, r *http.Request) {
	cfg := api.NewBrandingOptionsDto()
	writeJSON(w, http.StatusOK, cfg)
}
