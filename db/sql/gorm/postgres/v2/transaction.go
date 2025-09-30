package v2

import (
	"github.com/gorganyio/gorgany/db/sql/core"
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
