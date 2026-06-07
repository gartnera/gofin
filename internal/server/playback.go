package server

import (
	"net/http"

	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/sj14/jellyfin-go/api"
)

func (s *Server) handlePlaybackInfo(w http.ResponseWriter, r *http.Request) {
	it := s.lookupItem(w, r)
	if it == nil {
		return
	}

	resp := api.NewPlaybackInfoResponse()
	resp.SetPlaySessionId(jellyfin.FormatID(it.ID))
	if jellyfin.IsPlayable(it.Kind) {
		resp.SetMediaSources([]api.MediaSourceInfo{jellyfin.MediaSource(it)})
	} else {
		resp.SetMediaSources([]api.MediaSourceInfo{})
	}
	writeJSON(w, http.StatusOK, resp)
}
