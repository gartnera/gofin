package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sj14/jellyfin-go/api"
)

// handleUpdateItem applies user-edited metadata to a single item, mirroring
// Jellyfin's POST /Items/{itemId} (the web metadata editor). It accepts a
// BaseItemDto, persists the fields gofin actually stores, and records the
// item's lock state so a later rescan won't clobber the edits. Admin only;
// returns 204 No Content like upstream.
//
// Genres, tags, studios and people are intentionally not handled: gofin doesn't
// index them, so accepting them would silently drop the values.
func (s *Server) handleUpdateItem(w http.ResponseWriter, r *http.Request) {
	it := s.lookupItem(w, r)
	if it == nil {
		return
	}

	var dto api.BaseItemDto
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	upd := it.Update()

	// Name is NotEmpty in the schema; reject a blank rename rather than fail the
	// write opaquely.
	if v, ok := dto.GetNameOk(); ok {
		name := strings.TrimSpace(*v)
		if name == "" {
			http.Error(w, "name must not be empty", http.StatusBadRequest)
			return
		}
		upd.SetName(name)
	}
	if v, ok := dto.GetSortNameOk(); ok {
		upd.SetSortName(*v)
	}
	if v, ok := dto.GetOverviewOk(); ok {
		upd.SetOverview(*v)
	}
	if v, ok := dto.GetProductionYearOk(); ok {
		upd.SetProductionYear(*v)
	}
	if v, ok := dto.GetIndexNumberOk(); ok {
		upd.SetIndexNumber(*v)
	}
	if v, ok := dto.GetParentIndexNumberOk(); ok {
		upd.SetParentIndexNumber(*v)
	}

	// Persist the lock state so RefreshItem / rescans honour the edits.
	if v, ok := dto.GetLockDataOk(); ok {
		upd.SetLockData(*v)
	}
	if fields, ok := dto.GetLockedFieldsOk(); ok {
		locked := make([]string, 0, len(fields))
		for _, f := range fields {
			locked = append(locked, string(f))
		}
		upd.SetLockedFields(locked)
	}

	if err := upd.Exec(r.Context()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
