package service

import (
	"errors"

	"github.com/osbits/gorgany/v2/app/core"
	dbcore "github.com/osbits/gorgany/v2/db/sql/core"
)

// newSession opens a session on the default datasource. One session per call:
// dbcore.ISession is bound transient, so injecting one into a singleton service
// would pin a single session for the life of the process.
func newSession(databases core.IDBContext) (dbcore.ISession, error) {
	source := databases.GetDataSource(core.DefaultKeyInRegistrar)
	if source == nil {
		return nil, errors.New("default datasource is not configured")
	}
	return source.NewSession()
}
