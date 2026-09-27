package v2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	"github.com/osbits/gorgany/v2/db/sql/core"
	"gorm.io/gorm"
)

// sessionImpl implements core.ISession for SQL Server.
//
// It holds no lock. The Postgres and MySQL sessions hold a mutex for the whole of a
// Transaction, which serialises every transaction on a session and deadlocks one that calls
// Transaction again from inside fn; gorm's handles are safe for concurrent use, and each
// transaction gets its own.
type sessionImpl struct {
	executor   *Executor
	dialect    core.SQLDialect
	dataSource core.IDataSource
}

// NewSession creates a new database session over the datasource's guarded connection.
func (ds *gormSQLServerDataSource) NewSession() (core.ISession, error) {
	session := ds.db.Session(&gorm.Session{})
	if session.Error != nil {
		return nil, session.Error
	}

	return &sessionImpl{
		executor:   NewExecutor(session),
		dialect:    ds.Dialect(),
		dataSource: ds,
	}, nil
}

func (s *sessionImpl) DataSource() core.IDataSource {
	return s.dataSource
}

// Executor returns the query executor for this session.
func (s *sessionImpl) Executor() core.IQueryExecutor {
	return s.executor
}

// Query returns a fresh query builder speaking the datasource's dialect, read_only included.
// It is never memoized: a memoized builder carries one query's clauses into the next.
func (s *sessionImpl) Query() core.IQueryBuilder {
	return builder.New(s.dialect)
}

// Transaction runs fn in a transaction, committing when fn returns nil and rolling back when
// it returns an error or panics.
//
// The transaction begins with ctx, so a cancelled ctx fails the BEGIN, and one cancelled
// later makes database/sql roll the transaction back. A panic in fn, or a runtime.Goexit such
// as a test's FailNow, rolls back first, so the connection is not returned to the pool with a
// transaction still open on it; the panic then carries on with its value and its stack as they
// were, which recovering and panicking again would lose. When the rollback after an error
// fails too, both errors are returned, joined; a rollback that finds the transaction already
// over — database/sql ends it when ctx is cancelled — is not a failure. With SET XACT_ABORT
// ON, which the connector sets, an error in any statement has already rolled the transaction
// back on the server, and the rollback here only tells the driver.
func (s *sessionImpl) Transaction(ctx context.Context, fn func(core.IDBTransaction) error) error {
	tx := s.executor.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return wrapServerError(tx.Error)
	}

	panicked := true
	defer func() {
		if panicked {
			tx.Rollback()
		}
	}()

	err := fn(&transactionImpl{
		Builder:  builder.New(s.dialect),
		Executor: NewExecutor(tx),
		dialect:  s.dialect,
	})
	panicked = false

	if err != nil {
		if rollbackErr := tx.Rollback().Error; rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("sqlserver: rolling back after the error failed: %w", rollbackErr))
		}
		return err
	}
	return wrapServerError(tx.Commit().Error)
}

// Close closes the session. gorm sessions need no explicit close.
func (s *sessionImpl) Close() error {
	return nil
}

// transactionImpl implements core.IDBTransaction.
type transactionImpl struct {
	*builder.Builder
	*Executor
	dialect core.SQLDialect
}

// Query returns a fresh query builder speaking the transaction's dialect.
func (t *transactionImpl) Query() core.IQueryBuilder {
	return builder.New(t.dialect)
}
