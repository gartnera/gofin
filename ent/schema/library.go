package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Library is a top-level, type-tagged media folder (movies, tvshows, music).
type Library struct {
	ent.Schema
}

// Fields of the Library.
func (Library) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		field.String("name").
			NotEmpty(),
		// type is the collection type: movies, tvshows or music.
		field.Enum("type").
			Values("movies", "tvshows", "music"),
		field.String("path").
			NotEmpty(),
	}
}

// Edges of the Library.
func (Library) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("items", MediaItem.Type),
	}
}
