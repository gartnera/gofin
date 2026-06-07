package server

import (
	"net/http"
	"os"
	"path/filepath"
)

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	it := s.lookupItem(w, r)
	if it == nil {
		return
	}
	if it.ImagePath == "" {
		http.Error(w, "no image", http.StatusNotFound)
		return
	}
	f, err := os.Open(it.ImagePath)
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
	http.ServeContent(w, r, filepath.Base(it.ImagePath), info.ModTime(), f)
}
