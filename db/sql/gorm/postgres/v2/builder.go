package v2

import (
	"github.com/osbits/gorgany/db/sql/builder"
	dbCore "github.com/osbits/gorgany/db/sql/core"
)

// The query builder used to live in this package with &PostgresDialect{} welded
// into its constructor. It is engine-agnostic once the dialect is injected, so
// the implementation now lives in db/sql/builder and this file is the Postgres
// entry point into it.
//
// Builder and Config are type aliases, not new types, so existing consumers that
// hold a *v2.Builder or type-assert to it keep compiling and keep working.
type (
	// Builder is an alias for builder.Builder.
	Builder = builder.Builder
	// Config is an alias for builder.Config.
	Config = builder.Config
)

// NewBuilder creates a query builder speaking PostgreSQL.
func NewBuilder() *Builder {
	return builder.New(&PostgresDialect{})
}

// NewBuilderWithDialect creates a query builder speaking an arbitrary dialect.
// This is the seam that used to be missing: reaching another engine previously
// meant forking the entire builder.
func NewBuilderWithDialect(dialect dbCore.SQLDialect) *Builder {
	return builder.New(dialect)
}
