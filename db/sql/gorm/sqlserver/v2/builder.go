package v2

import (
	"github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// Builder is an alias for the engine-agnostic builder, as in the Postgres and MySQL packages,
// so the three engines read the same way at call sites.
type Builder = builder.Builder

// NewBuilder creates a query builder speaking T-SQL, for a datasource that is not read-only.
// A session hands out builders with the datasource's own dialect; use one of those where there
// is a session, so a read_only datasource's builders refuse writes.
func NewBuilder() *Builder {
	return builder.New(&SQLServerDialect{})
}

// NewBuilderWithDialect creates a query builder speaking an arbitrary dialect, such as a
// SQLServerDialect{ReadOnly: true}.
func NewBuilderWithDialect(dialect dbCore.SQLDialect) *Builder {
	return builder.New(dialect)
}
