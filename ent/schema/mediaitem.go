package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// MediaItem is a node in the Jellyfin item hierarchy. Folder-like kinds
// (Series, Season, MusicArtist, MusicAlbum) have no path; playable kinds
// (Movie, Episode, Audio) point at a file on disk.
type MediaItem struct {
	ent.Schema
}

// Fields of the MediaItem.
func (MediaItem) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		// kind maps to the Jellyfin BaseItemKind.
		field.Enum("kind").
			Values("Movie", "Series", "Season", "Episode", "MusicArtist", "MusicAlbum", "Audio"),
		field.String("name").
			NotEmpty(),
		field.String("sort_name").
			Default(""),
		// path is the absolute file path for playable items; empty for folders.
		field.String("path").
			Default(""),
		field.String("container").
			Default(""),
		field.Int64("run_time_ticks").
			Default(0),
		field.Int32("production_year").
			Optional().
			Nillable(),
		field.Int32("index_number").
			Optional().
			Nillable(),
		field.Int32("parent_index_number").
			Optional().
			Nillable(),
		field.String("overview").
			Default(""),
		field.String("album_artist").
			Default(""),
		// image_path is an optional poster/cover file on disk.
		field.String("image_path").
			Default(""),
	}
}

// Edges of the MediaItem.
func (MediaItem) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("library", Library.Type).
			Ref("items").
			Unique(),
		edge.To("children", MediaItem.Type).
			From("parent").
			Unique(),
	}
}

// Indexes of the MediaItem.
func (MediaItem) Indexes() []ent.Index {
	return nil
}
