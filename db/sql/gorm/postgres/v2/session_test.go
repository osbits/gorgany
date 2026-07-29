package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSession builds a sessionImpl directly. Query() only needs the dialect,
// so no server and no *gorm.DB are involved — this is the whole point of pulling
// the dialect onto the session.
func newTestSession(dialect dbCore.SQLDialect) *sessionImpl {
	return &sessionImpl{dialect: dialect}
}

// TestSessionQueryReturnsFreshBuilder is the T2.3 regression. Query() memoized
// one builder per session and returned the same instance every time, so a second
// Query() arrived already carrying the first query's WHERE and ORDER BY — wrong
// results, silently, with no error anywhere.
func TestSessionQueryReturnsFreshBuilder(t *testing.T) {
	s := newTestSession(&PostgresDialect{})

	first := s.Query()
	second := s.Query()

	assert.NotSame(t, first, second, "each Query() must hand back a distinct builder")
}

// TestSessionQueryDoesNotLeakClauseState is the same defect stated as the symptom
// an app author would actually observe.
func TestSessionQueryDoesNotLeakClauseState(t *testing.T) {
	s := newTestSession(&PostgresDialect{})

	// A first query narrows to one tenant and sorts.
	firstSQL, firstArgs, err := s.Query().
		Select("id").
		From("orders").
		Eq("tenant_id", 42).
		OrderBy("created_at", "desc").
		ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT id FROM orders WHERE tenant_id = ? ORDER BY "created_at" DESC`, firstSQL)
	assert.Equal(t, []any{42}, firstArgs)

	// A second, unrelated query on the same session must start clean.
	secondSQL, secondArgs, err := s.Query().
		Select("id").
		From("widgets").
		ToSQL()
	require.NoError(t, err)

	assert.Equal(t, "SELECT id FROM widgets", secondSQL)
	assert.Empty(t, secondArgs)
	assert.NotContains(t, secondSQL, "tenant_id", "the previous query's WHERE must not leak")
	assert.NotContains(t, secondSQL, "ORDER BY", "the previous query's ORDER BY must not leak")
	assert.NotContains(t, secondSQL, "orders", "the previous query's FROM must not leak")
}

// TestSessionQueryUsesSessionDialect proves the dialect is threaded
// datasource -> session -> builder rather than hard-coded in the builder.
func TestSessionQueryUsesSessionDialect(t *testing.T) {
	pg := newTestSession(&PostgresDialect{})
	assert.Equal(t, "postgres", pg.Query().Dialect().Name())
}

// TestTransactionQueryReturnsFreshBuilder covers the same defect on the
// transaction path, which returned the one embedded builder.
func TestTransactionQueryReturnsFreshBuilder(t *testing.T) {
	tx := &transactionImpl{
		Builder: NewBuilder(),
		dialect: &PostgresDialect{},
	}

	first := tx.Query()
	second := tx.Query()

	assert.NotSame(t, first, second)

	firstSQL, _, err := first.Select("id").From("a").Eq("x", 1).ToSQL()
	require.NoError(t, err)
	assert.Contains(t, firstSQL, "FROM a")

	secondSQL, secondArgs, err := second.Select("id").From("b").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM b", secondSQL)
	assert.Empty(t, secondArgs)
}

// TestPostgresDataSourceExposesDialect pins the DialectAware contract sessions
// rely on.
func TestPostgresDataSourceExposesDialect(t *testing.T) {
	var ds any = &gormPostgresDataSource{}

	aware, ok := ds.(DialectAware)
	require.True(t, ok, "the Postgres datasource must expose its dialect")
	assert.Equal(t, "postgres", aware.Dialect().Name())
}
