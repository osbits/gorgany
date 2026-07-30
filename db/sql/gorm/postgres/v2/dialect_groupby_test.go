package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatGroupBy_PlainFields is the unchanged baseline.
func TestFormatGroupBy_PlainFields(t *testing.T) {
	d := &PostgresDialect{}

	sql, args, err := d.FormatGroupBy(&dbCore.GroupByClause{Fields: []string{"status", "type"}})

	require.NoError(t, err)
	assert.Equal(t, "GROUP BY status, type", sql)
	assert.Empty(t, args)
}

// TestFormatGroupBy_RollupCubeAndSets pins the grouping-set modifiers. They were
// stored on the clause by Builder.Rollup/Cube/GroupingSets but the dialect only
// ever read Fields, so they were silently dropped.
func TestFormatGroupBy_RollupCubeAndSets(t *testing.T) {
	d := &PostgresDialect{}

	tests := []struct {
		name    string
		groupBy *dbCore.GroupByClause
		want    string
	}{
		{
			name:    "ROLLUP only",
			groupBy: &dbCore.GroupByClause{Rollup: []string{"region", "city"}},
			want:    "GROUP BY ROLLUP (region, city)",
		},
		{
			name:    "CUBE only",
			groupBy: &dbCore.GroupByClause{Cube: []string{"region", "city"}},
			want:    "GROUP BY CUBE (region, city)",
		},
		{
			name:    "GROUPING SETS only",
			groupBy: &dbCore.GroupByClause{Sets: [][]string{{"region"}, {"region", "city"}}},
			want:    "GROUP BY GROUPING SETS ((region), (region, city))",
		},
		{
			name: "fields plus ROLLUP",
			groupBy: &dbCore.GroupByClause{
				Fields: []string{"year"},
				Rollup: []string{"region", "city"},
			},
			want: "GROUP BY year, ROLLUP (region, city)",
		},
		{
			name:    "empty clause emits nothing",
			groupBy: &dbCore.GroupByClause{},
			want:    "",
		},
		{
			name:    "nil clause emits nothing",
			groupBy: nil,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, _, err := d.FormatGroupBy(tt.groupBy)
			require.NoError(t, err)
			assert.Equal(t, tt.want, sql)
		})
	}
}

// TestRollupThroughBuilderNoLongerEmitsBareGroupBy is the end-to-end regression.
// Rollup() with no plain GroupBy() fields used to render "GROUP BY " — a
// syntax error the server rejected.
func TestRollupThroughBuilderNoLongerEmitsBareGroupBy(t *testing.T) {
	sql, _, err := NewBuilder().
		Select("region", "SUM(amount)").
		From("sales").
		Rollup("region").
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "SELECT region, SUM(amount) FROM sales GROUP BY ROLLUP (region)", sql)
	assert.NotContains(t, sql, "GROUP BY  ")
	assert.False(t, hasSuffixTrimmed(sql, "GROUP BY"), "must never end on a bare GROUP BY")
}

func hasSuffixTrimmed(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// TestSetClauseOrderIsDeterministic pins the fix for map-range ordering: UPDATE
// SET and ON CONFLICT DO UPDATE SET are generated from a map, so column order
// used to differ on every call. That never corrupted results — args are appended
// in the same pass — but it made the SQL untestable and defeated statement caches.
func TestSetClauseOrderIsDeterministic(t *testing.T) {
	build := func() (string, []any) {
		sql, args, err := NewBuilder().
			Update("users").
			SetMap(map[string]interface{}{"zeta": 3, "alpha": 1, "mid": 2}).
			ToSQL()
		require.NoError(t, err)
		return sql, args
	}

	sql, args := build()
	assert.Equal(t, "UPDATE users SET alpha = ?, mid = ?, zeta = ?", sql)
	assert.Equal(t, []any{1, 2, 3}, args)

	for i := 0; i < 25; i++ {
		gotSQL, gotArgs := build()
		assert.Equal(t, sql, gotSQL, "SET clause order must be stable across calls")
		assert.Equal(t, args, gotArgs)
	}
}

func TestOnConflictDoUpdateOrderIsDeterministic(t *testing.T) {
	build := func() (string, []any) {
		sql, args, err := NewBuilder().
			Insert("users").
			Columns("id", "name").
			Values(1, "ann").
			OnConflict("id").
			DoUpdate(map[string]interface{}{"zeta": 3, "alpha": 1}).
			ToSQL()
		require.NoError(t, err)
		return sql, args
	}

	sql, args := build()
	assert.Equal(t,
		"INSERT INTO users (id, name) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET alpha = ?, zeta = ?",
		sql)
	assert.Equal(t, []any{1, "ann", 1, 3}, args)

	for i := 0; i < 25; i++ {
		gotSQL, gotArgs := build()
		assert.Equal(t, sql, gotSQL)
		assert.Equal(t, args, gotArgs)
	}
}
