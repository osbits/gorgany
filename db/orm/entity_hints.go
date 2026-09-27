package orm

// CascadingSaves is implemented by a model that wants Save to write its related entities
// on an external_schema datasource.
//
// Save cascades: saving an entity also saves every related entity it holds, and rewrites the
// join rows of its many-to-many relations. On a datasource gorgany owns that is a
// convenience. On one whose schema another system owns (external_schema: true) — tables an
// EF Core model created, say — the related tables belong to that system too, and it usually
// has rules about them gorgany cannot see: triggers, audit columns, rows it expects to write
// itself, a join table it manages. A cascade there writes into tables the caller never
// named, so it is refused, and the refusal wraps core.ErrExternalSchema.
//
// The refusal is the default because the damage is silent and lands in someone else's
// data. A model whose related tables really are safe to write through the ORM says so by
// returning true, which restores the cascade for that model only. It is asked of each
// entity the cascade would reach, so a related entity's own relations cascade only if it opts
// in too, and it is asked of all of them before anything is written, so a refusal anywhere in
// the cascade leaves every row as it was. Returning false is the same as not implementing it. Datasources gorgany owns cascade as
// they always have, whatever the model says.
type CascadingSaves interface {
	CascadeSaves() bool
}
