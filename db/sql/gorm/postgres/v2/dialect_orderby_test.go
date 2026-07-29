package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatOrderBy_QuotesSimpleIdentifiers(t *testing.T) {
	d := &PostgresDialect{}

	sql, args, err := d.FormatOrderBy("created_at", "desc")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY "created_at" DESC`, sql)
	assert.Empty(t, args)

	sql, _, err = d.FormatOrderBy("name", "asc")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY "name" ASC`, sql)
}

func TestFormatOrderBy_QuotesDottedIdentifiers(t *testing.T) {
	d := &PostgresDialect{}
	sql, args, err := d.FormatOrderBy("members.created_at", "asc")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY "members"."created_at" ASC`, sql)
	assert.Empty(t, args)
}

func TestFormatOrderBy_NormalizesDirection(t *testing.T) {
	d := &PostgresDialect{}

	// unknown / malicious direction falls back to ASC
	sql, _, err := d.FormatOrderBy("id", "asc; DROP TABLE users")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY "id" ASC`, sql)

	// case-insensitive desc
	sql, _, err = d.FormatOrderBy("id", "DeSc")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY "id" DESC`, sql)

	// empty direction defaults to ASC
	sql, _, err = d.FormatOrderBy("id", "")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY "id" ASC`, sql)
}

// A non-identifier field (the shape of a sub-select / boolean- or time-based
// payload) must never be interpolated into the SQL. It is bound as a placeholder,
// so it can only ever be a constant sort key — never executable SQL. The direction
// is still whitelisted.
func TestFormatOrderBy_NonIdentifierIsBound(t *testing.T) {
	d := &PostgresDialect{}

	sql, args, err := d.FormatOrderBy("(SELECT CASE WHEN (SELECT 1)=1 THEN name ELSE id END)", "desc")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY ? DESC`, sql)
	assert.Equal(t, []interface{}{"(SELECT CASE WHEN (SELECT 1)=1 THEN name ELSE id END)"}, args)

	sql, args, err = d.FormatOrderBy("first_name || ' ' || last_name", "asc")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY ? ASC`, sql)
	assert.Equal(t, []interface{}{"first_name || ' ' || last_name"}, args)
}

// orderByField with Raw=true emits a trusted expression verbatim (no quoting, no
// binding), so legitimate expression ordering keeps working when the caller opts in.
func TestOrderByField_RawIsVerbatim(t *testing.T) {
	d := &PostgresDialect{}
	sql, args := d.orderByField(dbCore.OrderByField{
		Field:     "first_name || ' ' || last_name",
		Direction: "ASC",
		Raw:       true,
	})
	assert.Equal(t, `first_name || ' ' || last_name ASC`, sql)
	assert.Empty(t, args)
}

// Multiple ORDER BY fields join into a single ORDER BY with comma separation,
// and each field's bound args thread through in order.
func TestFormatQuery_MultipleOrderByFields(t *testing.T) {
	d := &PostgresDialect{}
	q := &dbCore.Query{
		From: &dbCore.FromClause{Table: "members"},
		Select: &dbCore.SelectClause{
			Fields: []string{"id"},
		},
		OrderBy: &dbCore.OrderByClause{
			Fields: []dbCore.OrderByField{
				{Field: "last_name", Direction: "asc"},
				{Field: "members.created_at", Direction: "desc"},
				{Field: "first_name || ' ' || last_name", Direction: "asc", Raw: true},
			},
		},
	}
	sql, _, err := d.FormatQuery(q)
	require.NoError(t, err)
	assert.Contains(t, sql, `ORDER BY "last_name" ASC, "members"."created_at" DESC, first_name || ' ' || last_name ASC`)
	// No repeated "ORDER BY" keyword.
	assert.Equal(t, 1, countSubstr(sql, "ORDER BY"))
}

func TestBuilder_OrderBy_BindsNonIdentifier(t *testing.T) {
	// Through the builder: an untrusted expression is bound, not interpolated.
	sql, args, err := NewBuilder().From("members").OrderBy("(SELECT 1)", "asc").ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "ORDER BY ? ASC")
	assert.Contains(t, args, "(SELECT 1)")
	assert.NotContains(t, sql, "(SELECT 1)")
}

func TestBuilder_OrderByRaw_Verbatim(t *testing.T) {
	sql, args, err := NewBuilder().From("members").OrderByRaw("first_name || ' ' || last_name", "asc").ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, `ORDER BY first_name || ' ' || last_name ASC`)
	assert.Empty(t, args)
}

func TestQuoteIdentifier(t *testing.T) {
	assert.Equal(t, `"id"`, quoteIdentifier("id"))
	assert.Equal(t, `"a"."b"`, quoteIdentifier("a.b"))
	// embedded double-quote is doubled
	assert.Equal(t, `"a""b"`, quoteIdentifier(`a"b`))
}

func countSubstr(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
