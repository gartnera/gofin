package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AccessToken is an opaque token issued on AuthenticateByName.
type AccessToken struct {
	ent.Schema
}

// Fields of the AccessToken.
func (AccessToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("token").
			Unique().
			NotEmpty(),
		field.String("client").
			Default(""),
		field.String("device").
			Default(""),
		field.String("device_id").
			Default(""),
		field.String("version").
			Default(""),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the AccessToken.
func (AccessToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("tokens").
			Unique().
			Required(),
	}
}
