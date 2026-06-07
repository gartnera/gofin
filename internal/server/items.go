package server

import (
	"net/http"
	"strings"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/library"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/sj14/jellyfin-go/api"
)

func (s *Server) handleUserViews(w http.ResponseWriter, r *http.Request) {
	libs, err := s.client.Library.Query().All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]api.BaseItemDto, 0, len(libs))
	for _, lib := range libs {
		views = append(views, jellyfin.MapLibraryView(lib, s.serverID))
	}
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(views))
}

// parseKinds converts a comma-separated IncludeItemTypes value into kinds.
func parseKinds(s string) []mediaitem.Kind {
	if s == "" {
		return nil
	}
	valid := map[string]mediaitem.Kind{
		"Movie":       mediaitem.KindMovie,
		"Series":      mediaitem.KindSeries,
		"Season":      mediaitem.KindSeason,
		"Episode":     mediaitem.KindEpisode,
		"MusicArtist": mediaitem.KindMusicArtist,
		"MusicAlbum":  mediaitem.KindMusicAlbum,
		"Audio":       mediaitem.KindAudio,
	}
	var kinds []mediaitem.Kind
	for _, part := range strings.Split(s, ",") {
		if k, ok := valid[strings.TrimSpace(part)]; ok {
			kinds = append(kinds, k)
		}
	}
	return kinds
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	parentID := q.Get("parentId")
	if parentID == "" {
		parentID = q.Get("ParentId")
	}
	recursive := q.Get("recursive") == "true" || q.Get("Recursive") == "true"
	kinds := parseKinds(firstNonEmptyQuery(q, "includeItemTypes", "IncludeItemTypes"))

	query := s.client.MediaItem.Query()

	if parentID != "" {
		id, err := jellyfin.ParseID(parentID)
		if err != nil {
			writeJSON(w, http.StatusOK, jellyfin.QueryResult(nil))
			return
		}
		// A parent may be a Library (top-level view) or a MediaItem (folder).
		if _, err := s.client.Library.Get(r.Context(), id); err == nil {
			query = query.Where(mediaitem.HasLibraryWith(library.ID(id)))
			if !recursive {
				query = query.Where(mediaitem.Not(mediaitem.HasParent()))
			}
		} else {
			query = query.Where(mediaitem.HasParentWith(mediaitem.ID(id)))
		}
	} else if recursive {
		// Recursive root query: leave unscoped (optionally kind-filtered below).
	} else {
		query = query.Where(mediaitem.Not(mediaitem.HasParent()))
	}

	if len(kinds) > 0 {
		query = query.Where(mediaitem.KindIn(kinds...))
	}

	items, err := query.
		WithParent().
		Order(ent.Asc(mediaitem.FieldSortName), ent.Asc(mediaitem.FieldName)).
		All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	dtos := make([]api.BaseItemDto, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, jellyfin.MapItem(it, s.serverID))
	}
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(dtos))
}

func (s *Server) handleItemByID(w http.ResponseWriter, r *http.Request) {
	it := s.lookupItem(w, r)
	if it == nil {
		return
	}
	writeJSON(w, http.StatusOK, jellyfin.MapItem(it, s.serverID))
}

// lookupItem resolves the {itemId} path value to a MediaItem with its parent
// edge loaded, writing a 404 and returning nil if not found.
func (s *Server) lookupItem(w http.ResponseWriter, r *http.Request) *ent.MediaItem {
	id, err := jellyfin.ParseID(r.PathValue("itemId"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil
	}
	it, err := s.client.MediaItem.Query().
		Where(mediaitem.ID(id)).
		WithParent().
		Only(r.Context())
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return nil
	}
	return it
}

func firstNonEmptyQuery(q map[string][]string, keys ...string) string {
	for _, k := range keys {
		if v := q[k]; len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return ""
}
