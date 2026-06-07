package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gartnera/gofin/internal/jellyfin"
)

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	it := s.lookupItem(w, r)
	if it == nil {
		return
	}
	if !jellyfin.IsPlayable(it.Kind) || it.Path == "" {
		http.Error(w, "not playable", http.StatusBadRequest)
		return
	}

	f, err := os.Open(it.Path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// ServeContent handles Range requests (seeking) and content-type detection
	// from the filename, giving us direct play for free.
	http.ServeContent(w, r, filepath.Base(it.Path), info.ModTime(), f)
}
