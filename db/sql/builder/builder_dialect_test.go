package builder_test

import (
	"errors"
	"testing"

	"github.com/osbits/gorgany/db/sql/builder"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDialect is a minimal SQLDialect that records that it was called and can be
// told to refuse. It exists to prove the builder renders through whatever dialect
// it was handed rather than a hard-coded one.
type stubDialect struct {
	name  string
	fail  error
	calls int
}

func (d *stubDialect) Name() string                     { return d.name }
func (d *stubDialect) QuoteIdentifier(id string) string { return "[" + id + "]" }

func (d *stubDialect) FormatQuery(q *dbCore.Query) (string, []any, error) {
	d.calls++
	if d.fail != nil {
		return "", nil, d.fail
	}
	table := ""
	if q.From != nil {
		table = q.From.Table
	}
	return d.name + ":" + table, []any{d.name}, nil
}

func (d *stubDialect) FormatSelect([]string, bool, []string) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatFrom(string, string) (string, []any, error) { return "", nil, nil }
func (d *stubDialect) FormatJoin(*dbCore.JoinClause) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatWhere(*dbCore.WhereClause) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatOrderBy(string, string) (string, []any, error) { return "", nil, nil }
func (d *stubDialect) FormatGroupBy(*dbCore.GroupByClause) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatHaving(*dbCore.HavingClause) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatLimit(int) (string, []any, error)  { return "", nil, nil }
func (d *stubDialect) FormatOffset(int) (string, []any, error) { return "", nil, nil }
func (d *stubDialect) FormatCTE(string, *dbCore.Query) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatUnion(*dbCore.Query, bool) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatWindow(string, *dbCore.WindowDefinition) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatSubquery(*dbCore.Query, string) (string, []any, error) {
	return "", nil, nil
}
func (d *stubDialect) FormatDistinctOn([]string) (string, []any, error) { return "", nil, nil }
func (d *stubDialect) FormatReturning([]string) (string, []any, error)  { return "", nil, nil }

var _ dbCore.SQLDialect = (*stubDialect)(nil)

// TestNewRendersThroughInjectedDialect is the core T2.1 regression: before v2 the
// builder welded &PostgresDialect{} into its constructor and the Config type that
// looked like the seam was never constructed or read, so reaching another engine
// meant forking the 886-line builder.
func TestNewRendersThroughInjectedDialect(t *testing.T) {
	d := &stubDialect{name: "stub"}

	sql, args, err := builder.New(d).From("widgets").ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "stub:widgets", sql)
	assert.Equal(t, []any{"stub"}, args)
	assert.Equal(t, 1, d.calls, "the injected dialect must be the one that renders")
}

func TestNewFromConfigRendersThroughInjectedDialect(t *testing.T) {
	d := &stubDialect{name: "cfg"}

	sql, _, err := builder.NewFromConfig(builder.Config{Dialect: d}).From("t").ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "cfg:t", sql)
}

func TestDialectAccessorReturnsInjectedDialect(t *testing.T) {
	d := &stubDialect{name: "stub"}
	assert.Same(t, d, builder.New(d).Dialect())
}

// TestNewRejectsNilDialect pins the construction-time failure. A nil dialect used
// to be silently acceptable and blew up much later inside ToSQL.
func TestNewRejectsNilDialect(t *testing.T) {
	assert.PanicsWithValue(t,
		"gorgany/db/sql/builder: New requires a non-nil dialect",
		func() { builder.New(nil) })

	assert.Panics(t, func() { builder.NewFromConfig(builder.Config{}) })
}

// TestToSQLPropagatesDialectError proves an unsupported construct surfaces as an
// error with empty SQL, rather than as SQL the server will reject.
func TestToSQLPropagatesDialectError(t *testing.T) {
	sentinel := dbCore.Unsupported("stub", "RETURNING", "use LAST_INSERT_ID()")
	d := &stubDialect{name: "stub", fail: sentinel}

	sql, args, err := builder.New(d).From("t").Returning("id").ToSQL()

	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel) || err == sentinel)
	assert.True(t, dbCore.IsUnsupported(err))
	assert.Empty(t, sql, "SQL must be empty on error, never partially rendered")
	assert.Nil(t, args)
}

// TestBuilderIsCopyOnWrite covers the contract every clause method relies on,
// including Insert — which used to mutate the receiver and return it, so an
// INSERT issued against a shared builder contaminated every later query.
func TestBuilderIsCopyOnWrite(t *testing.T) {
	d := &stubDialect{name: "stub"}
	base := builder.New(d)

	base.From("users")
	base.Insert("audit_log")
	base.Where(&dbCore.BinaryCondition{Left: "id", Operator: "=", Right: 1})

	q := base.Build()
	assert.Nil(t, q.From, "From must not mutate the receiver")
	assert.Nil(t, q.Insert, "Insert must not mutate the receiver")
	assert.Nil(t, q.Where, "Where must not mutate the receiver")

	// The returned clone does carry the clause.
	assert.Equal(t, "audit_log", base.Insert("audit_log").Build().Insert.Table)
}
