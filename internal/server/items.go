package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/library"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/ent/playstate"
	"github.com/gartnera/gofin/ent/user"
	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/google/uuid"
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
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(views, len(views), 0))
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

// sortOrderField maps a Jellyfin SortBy value to an ent ordering option.
func sortOrderField(sortBy string, desc bool) mediaitem.OrderOption {
	dir := ent.Asc
	if desc {
		dir = ent.Desc
	}
	switch sortBy {
	case "Name":
		return dir(mediaitem.FieldName)
	case "ProductionYear", "PremiereDate":
		return dir(mediaitem.FieldProductionYear)
	case "IndexNumber":
		return dir(mediaitem.FieldIndexNumber)
	default: // SortName and anything unrecognised
		return dir(mediaitem.FieldSortName)
	}
}

func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	parentID := firstNonEmptyQuery(q, "parentId", "ParentId")
	recursive := q.Get("recursive") == "true" || q.Get("Recursive") == "true"
	kinds := parseKinds(firstNonEmptyQuery(q, "includeItemTypes", "IncludeItemTypes"))
	search := firstNonEmptyQuery(q, "searchTerm", "SearchTerm")

	query := s.client.MediaItem.Query()

	if parentID != "" {
		id, err := jellyfin.ParseID(parentID)
		if err != nil {
			writeJSON(w, http.StatusOK, jellyfin.QueryResult(nil, 0, 0))
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
	} else if !recursive {
		query = query.Where(mediaitem.Not(mediaitem.HasParent()))
	}

	if len(kinds) > 0 {
		query = query.Where(mediaitem.KindIn(kinds...))
	}
	if search != "" {
		query = query.Where(mediaitem.NameContainsFold(search))
	}

	// Total before paging.
	total, err := query.Clone().Count(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	desc := strings.EqualFold(firstNonEmptyQuery(q, "sortOrder", "SortOrder"), "Descending")
	query = query.Order(sortOrderField(firstNonEmptyQuery(q, "sortBy", "SortBy"), desc), ent.Asc(mediaitem.FieldName))

	startIndex := atoiDefault(firstNonEmptyQuery(q, "startIndex", "StartIndex"), 0)
	if startIndex > 0 {
		query = query.Offset(startIndex)
	}
	if limit := atoiDefault(firstNonEmptyQuery(q, "limit", "Limit"), 0); limit > 0 {
		query = query.Limit(limit)
	}

	items, err := query.WithParent().All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	dtos := s.mapItems(r.Context(), userFrom(r.Context()), items)
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(dtos, total, startIndex))
}

// mapItems maps items to DTOs, batch-loading the user's play states so each
// item carries the correct UserData.
func (s *Server) mapItems(ctx context.Context, u *ent.User, items []*ent.MediaItem) []api.BaseItemDto {
	states := s.playStates(ctx, u, items)
	dtos := make([]api.BaseItemDto, 0, len(items))
	for _, it := range items {
		dtos = append(dtos, jellyfin.MapItem(it, s.serverID, states[it.ID]))
	}
	return dtos
}

// playStates returns the user's play states for the given items, keyed by item id.
func (s *Server) playStates(ctx context.Context, u *ent.User, items []*ent.MediaItem) map[uuid.UUID]*ent.PlayState {
	out := map[uuid.UUID]*ent.PlayState{}
	if u == nil || len(items) == 0 {
		return out
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	states, err := s.client.PlayState.Query().
		Where(
			playstate.HasUserWith(user.ID(u.ID)),
			playstate.HasItemWith(mediaitem.IDIn(ids...)),
		).
		WithItem().
		All(ctx)
	if err != nil {
		return out
	}
	for _, ps := range states {
		if ps.Edges.Item != nil {
			out[ps.Edges.Item.ID] = ps
		}
	}
	return out
}

func (s *Server) handleItemByID(w http.ResponseWriter, r *http.Request) {
	it := s.lookupItem(w, r)
	if it == nil {
		return
	}
	ps := s.playState(r.Context(), userFrom(r.Context()), it.ID)
	writeJSON(w, http.StatusOK, jellyfin.MapItem(it, s.serverID, ps))
}

func (s *Server) handleResumeItems(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	items, err := s.client.MediaItem.Query().
		Where(mediaitem.HasPlaystatesWith(
			playstate.HasUserWith(user.ID(u.ID)),
			playstate.PlayedEQ(false),
			playstate.PlaybackPositionTicksGT(0),
		)).
		WithParent().
		All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dtos := s.mapItems(r.Context(), u, items)
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(dtos, len(dtos), 0))
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

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
