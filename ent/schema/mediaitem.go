package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/gartnera/gofin/internal/metadata"
	"github.com/gartnera/gofin/internal/nfo"
	"github.com/gartnera/gofin/internal/probe"
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
		// mtime and size capture the on-disk state at index time so a rescan can
		// skip files that have not changed (avoiding a re-probe).
		field.Int64("mtime").
			Default(0),
		field.Int64("size").
			Default(0),
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
		// index_number_end is the final episode number for a multi-episode file
		// (e.g. "S01E01-E02"); nil for single episodes.
		field.Int32("index_number_end").
			Optional().
			Nillable(),
		field.Int32("parent_index_number").
			Optional().
			Nillable(),
		field.String("overview").
			Default(""),
		// The following fields are populated from local NFO sidecar metadata
		// (internal/nfo) when present; empty/nil when no NFO supplied them.
		field.String("tagline").
			Default(""),
		field.String("official_rating").
			Default(""),
		field.Float32("community_rating").
			Optional().
			Nillable(),
		field.Time("premiere_date").
			Optional().
			Nillable(),
		field.Strings("genres").
			Optional(),
		field.Strings("studios").
			Optional(),
		field.JSON("people", []nfo.Person{}).
			Optional(),
		field.String("album_artist").
			Default(""),
		// image_path is an optional poster/cover file on disk.
		field.String("image_path").
			Default(""),
		// media_streams holds probed stream metadata (codecs/resolution) for
		// playable items.
		field.JSON("media_streams", []probe.Stream{}).
			Optional(),
		// lock_data mirrors Jellyfin's BaseItemDto.LockData ("Lock this item to
		// prevent future changes"): when true the scanner preserves every
		// user-editable metadata field instead of re-deriving it on rescan.
		field.Bool("lock_data").
			Default(false),
		// locked_fields mirrors Jellyfin's BaseItemDto.LockedFields: the set of
		// MetadataField names (e.g. "Name", "Overview") the scanner must not
		// overwrite even when the item as a whole isn't locked.
		field.JSON("locked_fields", []string{}).
			Optional(),
		// provider_ids records the remote-metadata identifiers resolved for this
		// item (e.g. {"Tmdb": "27205"}). Stamped once so a movie/series is looked
		// up a single time and reused across episodes and rescans.
		field.JSON("provider_ids", metadata.ProviderIDs{}).
			Optional(),
		// metadata_synced_at marks when remote enrichment last completed for this
		// item. Nil means "not yet enriched": the background enricher's startup
		// and periodic sweep enqueue every nil item, so enrichment survives
		// restarts and dropped channel sends. Set on success or a definitive
		// not-found (and on locked items, which are never re-swept).
		field.Time("metadata_synced_at").
			Optional().
			Nillable(),
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
		edge.To("playstates", PlayState.Type),
	}
}

// Indexes of the MediaItem.
func (MediaItem) Indexes() []ent.Index {
	return []ent.Index{
		// path is the dedup lookup key for playable items during scans.
		index.Fields("path"),
		// Listing a library's items by kind, sorted by name, is the hottest
		// query path (the web client's library grids). ent appends the edge FK
		// after the fields, so the index is (kind, sort_name, library_items):
		// kind seeks, sort_name supplies the order without a filesort, and the
		// library scope is a residual filter (cheap, since a library only holds
		// items of one kind family).
		index.Fields("kind", "sort_name").
			Edges("library"),
		// Folder browsing (Series -> Seasons, Season -> Episodes, Album ->
		// tracks, and the nested "all episodes of a series" query) filters on the
		// parent FK, and the prune pass counts children per parent. ent always
		// orders index fields before edge columns, so a Fields(...).Edges("parent")
		// composite would bury the parent FK as a trailing column — useless as a
		// seek key, yet still liable to be chosen by the planner (measurably
		// slower). An edge-only index leads with the parent FK, which is exactly
		// what these equality lookups need.
		index.Edges("parent"),
		// Likewise an edge-only library index leads with the library FK for the
		// HasLibraryWith equality scoping used by counts and the Latest query.
		index.Edges("library"),
		// "Latest" carousels order a library's items by mtime descending.
		index.Fields("mtime").
			Edges("library"),
		// The enricher's sweep finds items not yet enriched (metadata_synced_at
		// IS NULL). After the initial pass almost every row is non-null, so an
		// index here keeps the periodic sweep from scanning the whole table.
		index.Fields("metadata_synced_at"),
	}
}
