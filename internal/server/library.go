package server

import (
	"context"
	"net/http"

	"github.com/gartnera/gofin/ent"
)

// handleRefreshLibraries triggers a rescan of every configured library. It
// mirrors Jellyfin's POST /Library/Refresh: the scan runs in the background and
// the call returns 204 immediately. Admin only.
func (s *Server) handleRefreshLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.client.Library.Query().All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Detach from the request context so the scan outlives the response.
	go s.scanLibraries(context.Background(), libs)
	w.WriteHeader(http.StatusNoContent)
}

// scanLibraries scans each library in turn, logging (via the scanner's own
// error returns) but not aborting the batch on a single failure.
func (s *Server) scanLibraries(ctx context.Context, libs []*ent.Library) {
	for _, lib := range libs {
		_ = s.scanner.ScanLibrary(ctx, lib)
	}
}

// handleRefreshItem re-probes and re-indexes the file backing a single item,
// mirroring Jellyfin's POST /Items/{itemId}/Refresh. Admin only.
func (s *Server) handleRefreshItem(w http.ResponseWriter, r *http.Request) {
	it := s.lookupItem(w, r)
	if it == nil {
		return
	}
	if err := s.scanner.RefreshItem(r.Context(), it); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
