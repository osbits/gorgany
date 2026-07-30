package v2

import (
	"github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// Builder is an alias for the engine-agnostic builder, mirroring the Postgres
// package so both engines read the same way at call sites.
type Builder = builder.Builder

// NewBuilder creates a query builder speaking MySQL.
func NewBuilder() *Builder {
	return builder.New(&MySQLDialect{})
}

// NewBuilderWithDialect creates a query builder speaking an arbitrary dialect.
func NewBuilderWithDialect(dialect dbCore.SQLDialect) *Builder {
	return builder.New(dialect)
}
