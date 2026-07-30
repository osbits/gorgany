package v2

import (
	"context"
	"sync"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	"github.com/osbits/gorgany/v2/db/sql/core"

	"gorm.io/gorm"
)

// sessionImpl implements core.ISession for MySQL.
type sessionImpl struct {
	executor   *Executor
	dialect    core.SQLDialect
	dataSource core.IDataSource
	mu         sync.Mutex
}

// NewSession creates a new database session.
func (ds *gormMySQLDataSource) NewSession() (core.ISession, error) {
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

// Query returns a fresh query builder speaking MySQL. Never memoized — see the
// Postgres session for why that was a bug.
func (s *sessionImpl) Query() core.IQueryBuilder {
	return builder.New(s.dialect)
}

// Transaction executes fn within a transaction.
func (s *sessionImpl) Transaction(ctx context.Context, fn func(core.IDBTransaction) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx := s.executor.db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	transaction := &transactionImpl{
		Builder:  builder.New(s.dialect),
		Executor: NewExecutor(tx),
		dialect:  s.dialect,
	}

	if err := fn(transaction); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// Close closes the session. GORM sessions need no explicit close.
func (s *sessionImpl) Close() error {
	return nil
}

// transactionImpl implements core.IDBTransaction.
type transactionImpl struct {
	*builder.Builder
	*Executor
	dialect core.SQLDialect
}

// Query returns a fresh query builder speaking MySQL.
func (t *transactionImpl) Query() core.IQueryBuilder {
	return builder.New(t.dialect)
}
