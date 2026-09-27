package orm

import (
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"gorm.io/gorm/schema"
)

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

// TableWithTriggers is implemented by a model whose table has enabled DML triggers that block
// RETURNING, to have Create and Update read generated columns back without it.
//
// Create reads the key and the other generated columns back in the INSERT itself, with
// RETURNING, where the dialect can express it, and Update reads a model's grgorm:"readback"
// fields in the UPDATE the same way. SQL Server spells RETURNING as OUTPUT INSERTED.<col>, and
// refuses an OUTPUT without INTO on a table with an enabled trigger on the statement's action
// (Msg 334): a trigger on INSERT refuses Create's, and one on UPDATE refuses the UPDATE's of a
// model with read-back fields. A trigger on DELETE alone refuses nothing the ORM sends. Nothing
// in the model says a table has such a trigger, and the refusal arrives only when the statement
// runs, so a model on such a table says so here.
//
// Create then inserts through the executor's ExecInsert, which on SQL Server reads the key with
// SCOPE_IDENTITY() in the same batch, the INSERT's own key and never one a trigger's INSERT
// generated, and reads any other generated column with a SELECT keyed on it. That learns a key
// the server generates only when it is the table's IDENTITY: a key from a default or a
// sequence, or on a table with an INSTEAD OF INSERT trigger, has to be assigned by the entity.
// Update re-reads the read-back fields with a SELECT after the UPDATE. Both SELECTs are
// statements of their own, so a write another session commits in between is what they read
// (see rereadReadbackFields).
//
// It is asked only where the dialect reports that triggers block its RETURNING (see
// core.ReturningBlockedByTriggers). Postgres's RETURNING works whatever triggers a table has,
// so a model shared between engines that returns true keeps RETURNING there. Returning false
// is the same as not implementing it. orm.VerifyModel reports a table whose triggers need it
// and a model that does not implement it, and a key Create cannot read back on a model that
// does.
type TableWithTriggers interface {
	TableHasTriggers() bool
}

// hasTriggers reports whether entity says its table has enabled triggers.
func hasTriggers(entity any) bool {
	withTriggers, ok := entity.(TableWithTriggers)
	return ok && withTriggers.TableHasTriggers()
}

// readbackFields returns the fields of s tagged grgorm:"readback", in the schema's field
// order, each once.
//
// The tag is matched as one whole comma-separated value. IsParamInTagExists would not do: it
// matches a substring, so grgorm:"noreadback", or any value that merely contains the word,
// would read a column back that nobody asked for. A field with no column, such as one tagged
// gorm:"-", has nothing to read and is left out.
func readbackFields(s *schema.Schema) []*schema.Field {
	if s == nil {
		return nil
	}
	var fields []*schema.Field
	seen := map[string]bool{}
	for _, field := range s.Fields {
		if field.DBName == "" || seen[field.DBName] || !hasORMTagValue(field.Tag.Get(core.GorganyORMTag), core.GorganyORMReadBack) {
			continue
		}
		seen[field.DBName] = true
		fields = append(fields, field)
	}
	return fields
}

// hasORMTagValue reports whether the comma-separated grgorm tag holds value exactly, ignoring
// the spaces around each part.
func hasORMTagValue(tag, value string) bool {
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == value {
			return true
		}
	}
	return false
}
