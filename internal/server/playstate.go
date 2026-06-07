package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/ent/mediaitem"
	"github.com/gartnera/gofin/ent/playstate"
	"github.com/gartnera/gofin/ent/user"
	"github.com/gartnera/gofin/internal/jellyfin"
	"github.com/google/uuid"
)

// playedThreshold is the fraction of an item that must be watched for it to be
// auto-marked as played when playback stops.
const playedThreshold = 0.9

// stateChange describes a play-state mutation. Nil fields are left unchanged.
type stateChange struct {
	position  *int64
	played    *bool
	bumpCount bool
}

// playState returns the user's play state for a single item, or nil.
func (s *Server) playState(ctx context.Context, u *ent.User, itemID uuid.UUID) *ent.PlayState {
	if u == nil {
		return nil
	}
	ps, err := s.client.PlayState.Query().
		Where(
			playstate.HasUserWith(user.ID(u.ID)),
			playstate.HasItemWith(mediaitem.ID(itemID)),
		).
		Only(ctx)
	if err != nil {
		return nil
	}
	return ps
}

// applyState creates or updates the user's play state for an item. It is a
// find-or-create rather than an ent upsert because the uniqueness is across two
// edges, which sqlite upserts don't express cleanly.
func (s *Server) applyState(ctx context.Context, u *ent.User, itemID uuid.UUID, ch stateChange) error {
	if u == nil {
		return nil
	}
	now := time.Now()
	existing := s.playState(ctx, u, itemID)
	if existing == nil {
		c := s.client.PlayState.Create().
			SetUserID(u.ID).
			SetItemID(itemID).
			SetLastPlayedDate(now)
		if ch.position != nil {
			c.SetPlaybackPositionTicks(*ch.position)
		}
		if ch.played != nil {
			c.SetPlayed(*ch.played)
		}
		if ch.bumpCount {
			c.SetPlayCount(1)
		}
		return c.Exec(ctx)
	}
	upd := existing.Update().SetLastPlayedDate(now)
	if ch.position != nil {
		upd.SetPlaybackPositionTicks(*ch.position)
	}
	if ch.played != nil {
		upd.SetPlayed(*ch.played)
	}
	if ch.bumpCount {
		upd.AddPlayCount(1)
	}
	return upd.Exec(ctx)
}

func int64Ptr(v int64) *int64 { return &v }
func boolPtr(v bool) *bool    { return &v }

// reportBody is the subset of the playback report bodies we read.
type reportBody struct {
	ItemId        string `json:"ItemId"`
	PositionTicks int64  `json:"PositionTicks"`
}

func decodeReport(r *http.Request) reportBody {
	var b reportBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&b)
	}
	// Query-param fallback for /PlayingItems/{itemId}/Progress style calls.
	if b.PositionTicks == 0 {
		b.PositionTicks = int64(atoiDefault(r.URL.Query().Get("positionTicks"), 0))
	}
	return b
}

// reportItem resolves the item id from the body field or the {itemId} path.
func reportItem(r *http.Request, body reportBody) (uuid.UUID, bool) {
	id := body.ItemId
	if id == "" {
		id = r.PathValue("itemId")
	}
	parsed, err := jellyfin.ParseID(id)
	if err != nil {
		return uuid.UUID{}, false
	}
	return parsed, true
}

func (s *Server) handlePlaybackStart(w http.ResponseWriter, r *http.Request) {
	body := decodeReport(r)
	if itemID, ok := reportItem(r, body); ok {
		_ = s.applyState(r.Context(), userFrom(r.Context()), itemID, stateChange{})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlaybackProgress(w http.ResponseWriter, r *http.Request) {
	body := decodeReport(r)
	if itemID, ok := reportItem(r, body); ok {
		_ = s.applyState(r.Context(), userFrom(r.Context()), itemID, stateChange{position: int64Ptr(body.PositionTicks)})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePlaybackStopped(w http.ResponseWriter, r *http.Request) {
	body := decodeReport(r)
	if itemID, ok := reportItem(r, body); ok {
		u := userFrom(r.Context())
		if s.nearEnd(r.Context(), itemID, body.PositionTicks) {
			// Watched to (near) the end: mark played and reset the resume point.
			_ = s.applyState(r.Context(), u, itemID, stateChange{played: boolPtr(true), position: int64Ptr(0), bumpCount: true})
		} else {
			_ = s.applyState(r.Context(), u, itemID, stateChange{position: int64Ptr(body.PositionTicks)})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// nearEnd reports whether position is within the played threshold of the item's
// runtime.
func (s *Server) nearEnd(ctx context.Context, itemID uuid.UUID, position int64) bool {
	it, err := s.client.MediaItem.Get(ctx, itemID)
	if err != nil || it.RunTimeTicks <= 0 {
		return false
	}
	return float64(position) >= float64(it.RunTimeTicks)*playedThreshold
}

func (s *Server) handleMarkPlayed(w http.ResponseWriter, r *http.Request) {
	s.markPlayed(w, r, true)
}

func (s *Server) handleMarkUnplayed(w http.ResponseWriter, r *http.Request) {
	s.markPlayed(w, r, false)
}

func (s *Server) markPlayed(w http.ResponseWriter, r *http.Request, played bool) {
	id, err := jellyfin.ParseID(r.PathValue("itemId"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	u := userFrom(r.Context())
	ch := stateChange{played: boolPtr(played)}
	if played {
		ch.position = int64Ptr(0)
		ch.bumpCount = true
	}
	if err := s.applyState(r.Context(), u, id, ch); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, jellyfin.UserDataFor(id, 0, s.playState(r.Context(), u, id)))
}
