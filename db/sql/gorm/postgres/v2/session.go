package v2

import (
	"context"
	"sync"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	"github.com/osbits/gorgany/v2/db/sql/core"

	"gorm.io/gorm"
)

// DialectAware is implemented by datasources that know which SQL dialect their
// connection speaks. Sessions use it to hand every builder the right dialect;
// a datasource that does not implement it falls back to Postgres.
type DialectAware interface {
	Dialect() core.SQLDialect
}

// sessionImpl implements the ISession interface
type sessionImpl struct {
	executor   *Executor
	dialect    core.SQLDialect
	dataSource core.IDataSource
	mu         sync.RWMutex
}

// NewSession creates a new database session
func (ds *gormPostgresDataSource) NewSession() (core.ISession, error) {
	// Create a new GORM session
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

// Executor returns the query executor for this session
func (s *sessionImpl) Executor() core.IQueryExecutor {
	return s.executor
}

// Query returns a fresh query builder speaking this session's dialect.
//
// It used to memoize one builder per session and hand the same instance back on
// every call, so a second Query() arrived carrying the first query's WHERE and
// ORDER BY state — silently wrong results rather than a crash. Every call now
// returns a new builder.
func (s *sessionImpl) Query() core.IQueryBuilder {
	return builder.New(s.dialect)
}

// Transaction executes the provided function within a transaction
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

// Close closes the session
func (s *sessionImpl) Close() error {
	// GORM sessions don't need explicit closing
	return nil
}
