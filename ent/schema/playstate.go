package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// PlayState records a user's playback state for a single media item
// (watched flag, resume position, play count).
type PlayState struct {
	ent.Schema
}

// Fields of the PlayState.
func (PlayState) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		field.Bool("played").
			Default(false),
		field.Int64("playback_position_ticks").
			Default(0),
		field.Int("play_count").
			Default(0),
		field.Time("last_played_date").
			Optional().
			Nillable(),
	}
}

// Edges of the PlayState.
func (PlayState) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("playstates").
			Unique().
			Required(),
		edge.From("item", MediaItem.Type).
			Ref("playstates").
			Unique().
			Required(),
	}
}
