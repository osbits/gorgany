package v2

import (
	"context"
	"sync"

	"git.qix.sx/gorgany/gorgany.git/db/sql/core"

	"gorm.io/gorm"
)

// sessionImpl implements the ISession interface
type sessionImpl struct {
	executor   *Executor
	query      *Builder
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

// Query creates a new query builder
func (s *sessionImpl) Query() core.IQueryBuilder {
	if s.query == nil {
		s.query = NewBuilder()
	}
	return s.query
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
		Builder:  NewBuilder(),
		Executor: NewExecutor(tx),
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
