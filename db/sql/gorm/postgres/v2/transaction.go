package v2

import (
	"git.qix.sx/gorgany/gorgany.git/db/sql/core"
)

// transactionImpl implements the IDBTransaction interface
type transactionImpl struct {
	*Builder
	*Executor
}

// Query creates a new query builder
func (t *transactionImpl) Query() core.IQueryBuilder {
	return t.Builder
}
