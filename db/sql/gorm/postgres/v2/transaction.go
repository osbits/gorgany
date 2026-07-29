package v2

import (
	"github.com/osbits/gorgany/db/sql/builder"
	"github.com/osbits/gorgany/db/sql/core"
)

// transactionImpl implements the IDBTransaction interface
type transactionImpl struct {
	*Builder
	*Executor
	dialect core.SQLDialect
}

// Query returns a fresh query builder speaking the transaction's dialect.
//
// Like sessionImpl.Query it used to hand back the one embedded builder, so a
// second Query() inside the same transaction inherited the first query's
// accumulated clauses.
func (t *transactionImpl) Query() core.IQueryBuilder {
	return builder.New(t.dialect)
}
