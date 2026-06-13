package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// MetadataCache persists a single remote-metadata lookup so it is never repeated
// across rescans or restarts. A row is keyed by (provider, kind, key): the
// normalized query (e.g. a movie title+year, or a series name) maps to one
// cached payload, which is also what gives gofin its "search our own library
// first" behavior — two items with the same title resolve to the same cached
// row instead of two remote calls. Misses are cached negatively (not_found) so
// an unmatched title is not re-searched on every scan.
type MetadataCache struct {
	ent.Schema
}

// Fields of the MetadataCache.
func (MetadataCache) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New),
		// provider is the metadata.Provider name (e.g. "Tmdb").
		field.String("provider"),
		// kind distinguishes lookup namespaces ("movie-search", "series-search").
		field.String("kind"),
		// key is the normalized lookup key within (provider, kind).
		field.String("key"),
		// payload is the JSON-encoded metadata.Result; empty when not_found.
		field.Bytes("payload").
			Optional(),
		// not_found records a negative result so the same miss is not re-searched.
		field.Bool("not_found").
			Default(false),
		// fetched_at is compared against the configured TTL at read time (the TTL
		// is not baked into the row, so changing it takes effect immediately).
		field.Time("fetched_at").
			Default(time.Now),
	}
}

// Indexes of the MetadataCache.
func (MetadataCache) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "kind", "key").
			Unique(),
	}
}
