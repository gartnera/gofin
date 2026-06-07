package server

import (
	"net/http"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/ent/predicate"
	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/sj14/jellyfin-go/api"
)

// handleSeasons returns the seasons of a Series (or an empty result if the
// path id doesn't resolve to a Series).
func (s *Server) handleSeasons(w http.ResponseWriter, r *http.Request) {
	id, err := jellyfin.ParseID(r.PathValue("seriesId"))
	if err != nil {
		writeJSON(w, http.StatusOK, jellyfin.QueryResult(nil, 0, 0))
		return
	}
	items, err := s.client.MediaItem.Query().
		Where(
			mediaitem.HasParentWith(mediaitem.ID(id)),
			mediaitem.KindEQ(mediaitem.KindSeason),
		).
		Order(ent.Asc(mediaitem.FieldIndexNumber), ent.Asc(mediaitem.FieldSortName)).
		WithParent(withGrandparent).
		All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dtos := s.mapItems(r.Context(), userFrom(r.Context()), items)
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(dtos, len(dtos), 0))
}

// handleEpisodes returns the episodes of a Series. When seasonId is supplied
// only the episodes for that season are returned.
func (s *Server) handleEpisodes(w http.ResponseWriter, r *http.Request) {
	seriesID, err := jellyfin.ParseID(r.PathValue("seriesId"))
	if err != nil {
		writeJSON(w, http.StatusOK, jellyfin.QueryResult(nil, 0, 0))
		return
	}
	q := r.URL.Query()

	predicates := []predicate.MediaItem{mediaitem.KindEQ(mediaitem.KindEpisode)}
	if seasonID := firstNonEmptyQuery(q, "seasonId", "SeasonId"); seasonID != "" {
		id, err := jellyfin.ParseID(seasonID)
		if err == nil {
			predicates = append(predicates, mediaitem.HasParentWith(mediaitem.ID(id)))
		}
	} else {
		// All episodes under the series. Rather than a doubly-nested
		// HasParentWith (episode -> season -> series), which SQLite plans poorly
		// at scale, resolve the season ids first and filter episodes by parent in
		// that set — a single-level parent lookup the (parent) index serves well.
		seasonIDs, err := s.client.MediaItem.Query().
			Where(mediaitem.KindEQ(mediaitem.KindSeason), mediaitem.HasParentWith(mediaitem.ID(seriesID))).
			IDs(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		predicates = append(predicates, mediaitem.HasParentWith(mediaitem.IDIn(seasonIDs...)))
	}

	items, err := s.client.MediaItem.Query().
		Where(predicates...).
		Order(
			ent.Asc(mediaitem.FieldParentIndexNumber),
			ent.Asc(mediaitem.FieldIndexNumber),
			ent.Asc(mediaitem.FieldSortName),
		).
		WithParent(withGrandparent).
		All(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dtos := s.mapItems(r.Context(), userFrom(r.Context()), items)
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(dtos, len(dtos), 0))
}

// handleSimilar returns an empty similar-items list — gofin doesn't compute
// recommendations.
func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(nil, 0, 0))
}

// handleThemeMedia returns the empty theme-media triple the client expects.
func (s *Server) handleThemeMedia(w http.ResponseWriter, r *http.Request) {
	empty := api.NewThemeMediaResult()
	empty.SetItems([]api.BaseItemDto{})
	empty.SetTotalRecordCount(0)
	empty.SetStartIndex(0)
	out := api.NewAllThemeMediaResult()
	out.SetThemeVideosResult(*empty)
	out.SetThemeSongsResult(*empty)
	out.SetSoundtrackSongsResult(*empty)
	writeJSON(w, http.StatusOK, out)
}

// handleEmptyQuery responds with an empty BaseItemDtoQueryResult — used to
// satisfy endpoints we don't implement (LiveTv/Programs etc).
func (s *Server) handleEmptyQuery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, jellyfin.QueryResult(nil, 0, 0))
}
