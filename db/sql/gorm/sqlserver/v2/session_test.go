package v2

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSession is a session over a fakeServer, as NewSession builds one over the datasource's
// connection.
func fakeSession(t *testing.T, server *fakeServer, readOnly bool) *sessionImpl {
	t.Helper()
	ds := &gormSQLServerDataSource{db: fakeGorm(t, server, readOnly, false), readOnly: readOnly}
	session, err := ds.NewSession()
	require.NoError(t, err)
	impl, ok := session.(*sessionImpl)
	require.True(t, ok)
	return impl
}

// TestSessionQueryReturnsFreshSQLServerBuilder: a memoized builder carries one query's
// clauses into the next.
func TestSessionQueryReturnsFreshSQLServerBuilder(t *testing.T) {
	s := fakeSession(t, &fakeServer{}, false)

	first := s.Query()
	second := s.Query()
	assert.NotSame(t, first, second)
	assert.Equal(t, DialectName, first.Dialect().Name())

	_, _, err := first.Select("Id").From("Orders").Eq("Id", 1).ToSQL()
	require.NoError(t, err)
	sql, args, err := second.Select("Id").From("Lines").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT [Id] FROM [Lines]", sql)
	assert.Empty(t, args)
}

// TestSessionBuildersCarryReadOnly: a read-only datasource's sessions hand out builders that
// refuse a write where it is built.
func TestSessionBuildersCarryReadOnly(t *testing.T) {
	s := fakeSession(t, &fakeServer{}, true)

	_, _, err := s.Query().Delete("Orders").Eq("Id", 1).ToSQL()
	require.ErrorIs(t, err, dbCore.ErrReadOnly)
	assert.NotNil(t, s.DataSource())
	assert.NoError(t, s.Close())
}

func TestTransactionQueryReturnsFreshSQLServerBuilder(t *testing.T) {
	server := &fakeServer{}
	s := fakeSession(t, server, false)

	require.NoError(t, s.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
		first := tx.Query()
		second := tx.Query()
		assert.NotSame(t, first, second)
		assert.Equal(t, DialectName, first.Dialect().Name())

		_, _, err := first.Select("Id").From("Orders").Eq("Id", 1).ToSQL()
		require.NoError(t, err)
		sql, _, err := second.Select("Id").From("Lines").ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT [Id] FROM [Lines]", sql)
		return nil
	}))
}

func TestTransactionCommitsOnSuccess(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{tail(1)}})
	s := fakeSession(t, server, false)

	require.NoError(t, s.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
		return tx.Exec(ctx, tx.Query().Update("Orders").Set("Status", "paid").Eq("Id", 1)).Error
	}))
	assert.Equal(t, []string{
		"begin",
		"query UPDATE [Orders] SET [Status] = @p1 WHERE [Id] = @p2; SELECT ROWCOUNT_BIG() AS [affected]",
		"commit",
	}, server.log())
}

func TestTransactionRollsBackOnError(t *testing.T) {
	server := &fakeServer{}
	s := fakeSession(t, server, false)
	failure := errors.New("the work failed")

	err := s.Transaction(ctx, func(dbCore.IDBTransaction) error { return failure })
	require.ErrorIs(t, err, failure)
	assert.Equal(t, []string{"begin", "rollback"}, server.log())
}

// TestTransactionRollsBackOnPanicAndRepanics: the connection must not go back to the pool with
// the transaction still open on it, and the panic must reach the caller unchanged.
func TestTransactionRollsBackOnPanicAndRepanics(t *testing.T) {
	server := &fakeServer{}
	s := fakeSession(t, server, false)

	assert.PanicsWithValue(t, "the work panicked", func() {
		_ = s.Transaction(ctx, func(dbCore.IDBTransaction) error { panic("the work panicked") })
	})
	assert.Equal(t, []string{"begin", "rollback"}, server.log())
}

type ctxKey struct{}

// TestTransactionPassesContextToBegin: the MySQL and Postgres sessions begin with no context,
// so a cancelled request still opened its transaction.
func TestTransactionPassesContextToBegin(t *testing.T) {
	server := &fakeServer{}
	s := fakeSession(t, server, false)

	withValue := context.WithValue(ctx, ctxKey{}, "request")
	require.NoError(t, s.Transaction(withValue, func(dbCore.IDBTransaction) error { return nil }))
	require.Len(t, server.beginContexts, 1)
	assert.Equal(t, "request", server.beginContexts[0].Value(ctxKey{}))

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	err := s.Transaction(cancelled, func(dbCore.IDBTransaction) error { called = true; return nil })
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, called, "fn must not run without a transaction")
}

// TestTransactionJoinsRollbackFailure: a failed rollback is reported alongside the error that
// caused it, not instead of it.
func TestTransactionJoinsRollbackFailure(t *testing.T) {
	rollbackFailure := errors.New("connection lost during rollback")
	server := &fakeServer{rollbackErr: rollbackFailure}
	s := fakeSession(t, server, false)
	failure := errors.New("the work failed")

	err := s.Transaction(ctx, func(dbCore.IDBTransaction) error { return failure })
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, err, rollbackFailure)
}

// TestTransactionIgnoresARollbackOfAFinishedTransaction: database/sql ends a transaction when
// its context is cancelled, and the rollback after that finds nothing to roll back.
func TestTransactionIgnoresARollbackOfAFinishedTransaction(t *testing.T) {
	server := &fakeServer{rollbackErr: sql.ErrTxDone}
	s := fakeSession(t, server, false)
	failure := errors.New("the work failed")

	err := s.Transaction(ctx, func(dbCore.IDBTransaction) error { return failure })
	assert.Equal(t, failure, err)
}

func TestTransactionReportsACommitFailure(t *testing.T) {
	commitFailure := errors.New("commit failed")
	server := &fakeServer{commitErr: commitFailure}
	s := fakeSession(t, server, false)

	require.ErrorIs(t, s.Transaction(ctx, func(dbCore.IDBTransaction) error { return nil }), commitFailure)
}

// TestTransactionsDoNotSerialise: the other engines hold a session mutex across the whole
// transaction, so a transaction that starts another on the same session deadlocks.
func TestTransactionsDoNotSerialise(t *testing.T) {
	server := &fakeServer{}
	s := fakeSession(t, server, false)

	done := make(chan error, 1)
	go func() {
		done <- s.Transaction(ctx, func(dbCore.IDBTransaction) error {
			return s.Transaction(ctx, func(dbCore.IDBTransaction) error { return nil })
		})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("a transaction started inside another on the same session deadlocked")
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, s.Transaction(ctx, func(dbCore.IDBTransaction) error { return nil }))
		}()
	}
	wg.Wait()
}
