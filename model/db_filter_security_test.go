package model

import (
	"context"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	sqlbuilder "github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	postgres "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEC-H08. DBFilter.Field was documented as "may contain raw SQL conditions", the caller's
// attributes were substituted into it by string replacement, and Field was emitted into the
// query as SQL syntax. Three facts that are individually defensible and together are second-
// order SQL injection through the authorization predicate itself.
//
// The attribute that makes it reachable is the username: it is chosen at registration, and
// `author_name = '{{user_username}}'` with `x' OR 'a'='a` becomes `author_name = 'x' OR
// 'a'='a'`. The placeholder count stays balanced, so nothing downstream notices, and because
// FormatWhere joins conditions with a bare AND/OR the injected OR reaches the siblings too.

// hostileUser is a user whose self-chosen attributes are SQL.
type hostileUser struct {
	id       string
	username string
	tenant   string
}

func (u *hostileUser) GetId() string          { return u.id }
func (u *hostileUser) GetUsername() string    { return u.username }
func (u *hostileUser) GetPassword() string    { return "" }
func (u *hostileUser) GetRole() core.UserRole { return core.UserRole("user") }
func (u *hostileUser) GetRoles() []string     { return []string{"user"} }
func (u *hostileUser) GetTenantID() string    { return u.tenant }

// renderFilters builds the SQL a filter set produces, through the real Postgres dialect.
func renderFilters(t *testing.T, filters []DBFilter) (string, []any, error) {
	t.Helper()

	builder := sqlbuilder.New(&postgres.PostgresDialect{}).Select("*").From("articles")
	applied, err := ApplyDBFiltersToQueryBuilder(builder, filters)
	if err != nil {
		return "", nil, err
	}
	return mustToSQL(t, applied)
}

func mustToSQL(t *testing.T, builder dbCore.IQueryBuilder) (string, []any, error) {
	t.Helper()
	sql, args, err := builder.ToSQL()
	return sql, args, err
}

// TestUsernameCannotEscapeThroughADBFilter is the headline, end to end.
func TestUsernameCannotEscapeThroughADBFilter(t *testing.T) {
	config := NewAccessControlConfig()
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		UserContextFields: map[string]string{"username": "{{.Username}}"},
		CustomFilters: map[string][]DBFilter{
			// The safe way to write what the vulnerable configuration was trying to express.
			"user": {{Field: "author_name", Operator: "=", Value: "{{.Username}}", Logic: "AND"}},
		},
	}

	attacker := &hostileUser{id: "u1", username: "x' OR 'a'='a"}
	rbac := rbacFor(config, attacker, nil)

	filters, err := rbac.GenerateDBFilters(context.Background(), "read")
	require.NoError(t, err)

	sql, args, err := renderFilters(t, filters)
	require.NoError(t, err)

	assert.NotContains(t, sql, "OR 'a'='a",
		"the attacker's value must not appear in the SQL text")
	assert.Contains(t, args, "x' OR 'a'='a",
		"it must appear in the argument list instead")
	assert.Equal(t, strings.Count(sql, "?"), len(args),
		"every placeholder is accounted for, so nothing was interpolated")
}

// TestDBFilterFieldRejectsSQL. Field is a column reference; anything else is refused at
// configuration time rather than emitted.
func TestDBFilterFieldRejectsSQL(t *testing.T) {
	payloads := []string{
		"author_name = 'x' OR 'a'='a'",
		"id; DROP TABLE articles",
		"id -- ",
		"id /* comment */",
		"1=1",
		"id OR 1=1",
		"id’",   // a unicode quote
		"id\nOR 1=1", // a newline
		"",
	}

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			filter := DBFilter{Field: payload, Operator: "=", Value: 1}
			assert.Error(t, filter.Validate(nil),
				"a field that is not a column reference must be refused")
		})
	}
}

// TestDBFilterFieldAcceptsRealColumns is the over-blocking fence.
func TestDBFilterFieldAcceptsRealColumns(t *testing.T) {
	for _, field := range []string{"id", "owner_id", "articles.author_id", "_private", "col$1"} {
		t.Run(field, func(t *testing.T) {
			assert.NoError(t, DBFilter{Field: field, Operator: "=", Value: 1}.Validate(nil))
		})
	}
}

// TestPlaceholdersAreNeverSubstitutedIntoAFieldOrRawPredicate. Substitution reaches values,
// and only values.
func TestPlaceholdersAreNeverSubstitutedIntoAFieldOrRawPredicate(t *testing.T) {
	config := NewAccessControlConfig()
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		UserContextFields: map[string]string{"username": "{{.Username}}"},
		CustomFilters: map[string][]DBFilter{
			"user": {{Field: "author_name = '{{.Username}}'", Operator: "=", Value: true}},
		},
	}

	// The username is the attacker's, exactly as it would be after registration.
	attacker := &hostileUser{id: "u1", username: "x' OR 'a'='a"}
	rbac := rbacFor(config, attacker, nil)

	filters, err := rbac.GenerateDBFilters(context.Background(), "read")
	if err == nil {
		// Not reached once the fix is in. Rendering here rather than only asserting on the
		// error is deliberate: when this test fails it should show the predicate the
		// configuration actually produced, not merely that no error came back.
		sql, args, renderErr := renderFilters(t, filters)
		t.Fatalf("a field carrying a placeholder must be refused, but it was accepted and "+
			"rendered as:\n  SQL:  %s\n  args: %v\n  render error: %v", sql, args, renderErr)
	}

	assert.ErrorContains(t, err, "raw_sql",
		"and the message has to say where raw SQL belongs")
}

// TestRawSQLFilterBindsItsArgs — the sanctioned escape hatch does what Field used to pretend
// to, safely.
func TestRawSQLFilterBindsItsArgs(t *testing.T) {
	config := NewAccessControlConfig()
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		UserContextFields: map[string]string{"username": "{{.Username}}"},
		CustomFilters: map[string][]DBFilter{
			"user": {{
				RawSQL:  "author_name = ? AND published = true",
				RawArgs: []any{"{{.Username}}"},
				Logic:   "AND",
			}},
		},
	}

	attacker := &hostileUser{id: "u1", username: "x' OR 'a'='a"}
	filters, err := rbacFor(config, attacker, nil).GenerateDBFilters(context.Background(), "read")
	require.NoError(t, err)

	sql, args, err := renderFilters(t, filters)
	require.NoError(t, err)

	assert.Contains(t, sql, "author_name = ?")
	assert.NotContains(t, sql, "OR 'a'='a")
	assert.Contains(t, args, "x' OR 'a'='a")
}

// TestAnUndeclaredPlaceholderIsRefused. Leaving it in would bind the literal braces, and
// before Field stopped being substituted into, would have put them into the SQL.
func TestAnUndeclaredPlaceholderIsRefused(t *testing.T) {
	config := NewAccessControlConfig()
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		CustomFilters: map[string][]DBFilter{
			"user": {{Field: "dept", Operator: "=", Value: "{{user_dept}}"}},
		},
	}

	_, err := rbacFor(config, &hostileUser{id: "u1"}, nil).GenerateDBFilters(
		context.Background(), "read")
	require.Error(t, err)
	assert.ErrorContains(t, err, "UserContextFields")
}

// TestAnUnknownUserContextFieldIsAnError. The default used to be the caller's user id, which
// is the most dangerous available answer for the fields this is used for: a mistyped tenant
// key compared the tenant column against a user id.
func TestAnUnknownUserContextFieldIsAnError(t *testing.T) {
	config := NewAccessControlConfig()
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		UserContextFields: map[string]string{"tennant_id": "{{.TennantID}}"}, // typo, deliberately
		CustomFilters: map[string][]DBFilter{
			"user": {{Field: "tenant_id", Operator: "=", Value: "{{.TennantID}}"}},
		},
	}

	_, err := rbacFor(config, &hostileUser{id: "u1", tenant: "t1"}, nil).GenerateDBFilters(
		context.Background(), "read")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "u1",
		"the diagnostic should name the misconfiguration, not silently use the user id")
}

// TestAnEmptyScopingAttributeIsRefused. `tenant_id = ''` is not "this caller's tenant".
func TestAnEmptyScopingAttributeIsRefused(t *testing.T) {
	config := NewAccessControlConfig()
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		UserContextFields: map[string]string{"tenant_id": "{{.TenantID}}"},
		CustomFilters: map[string][]DBFilter{
			"user": {{Field: "tenant_id", Operator: "=", Value: "{{.TenantID}}"}},
		},
	}

	_, err := rbacFor(config, &hostileUser{id: "u1", tenant: ""}, nil).GenerateDBFilters(
		context.Background(), "read")
	assert.Error(t, err)
}

// TestAnUnknownOperatorIsAnErrorNotADroppedFilter. A restriction that disappears stops
// restricting.
func TestAnUnknownOperatorIsAnErrorNotADroppedFilter(t *testing.T) {
	_, _, err := renderFilters(t, []DBFilter{{Field: "owner_id", Operator: "regexp", Value: "x"}})
	assert.Error(t, err)
}

// TestTheInOperatorAcceptsAStringSlice. A list decoded from YAML or JSON arrives as []string,
// which failed the []interface{} assertion and dropped the whole filter.
func TestTheInOperatorAcceptsAStringSlice(t *testing.T) {
	sql, args, err := renderFilters(t,
		[]DBFilter{{Field: "owner_id", Operator: "in", Value: []string{"a", "b"}}})
	require.NoError(t, err)

	assert.Contains(t, sql, "IN (?, ?)")
	assert.Equal(t, []any{"a", "b"}, args)
}

// TestAnEmptyInListIsAnError — `IN ()` is not valid SQL, and silently dropping the filter is
// how the restriction disappears.
func TestAnEmptyInListIsAnError(t *testing.T) {
	_, _, err := renderFilters(t, []DBFilter{{Field: "owner_id", Operator: "in", Value: []string{}}})
	assert.Error(t, err)
}

// TestADBJoinComparesColumnsRatherThanBindingOne. The right side of a BinaryCondition is a
// value position, so a bare string was bound — every DBJoin-based filter joined on the *name*
// of the other column.
func TestADBJoinComparesColumnsRatherThanBindingOne(t *testing.T) {
	sql, args, err := renderFilters(t, []DBFilter{{
		Field:    "team_members.user_id",
		Operator: "=",
		Value:    "u1",
		Join: &DBJoin{
			Type: "INNER", Table: "teams",
			LeftKey: "team_members.team_id", RightKey: "teams.id",
		},
	}})
	require.NoError(t, err)

	assert.Contains(t, sql, "team_members.team_id = teams.id",
		"the join must compare the two columns")
	assert.NotContains(t, args, "teams.id",
		"and must not bind the second column's name as a value")
}

// TestTheNoAccessFilterAdmitsNothingWithoutAssumingAnIdColumn. The old sentinel was
// `id = -1`, which assumes the table has an id and that -1 is not a valid one.
func TestTheNoAccessFilterAdmitsNothingWithoutAssumingAnIdColumn(t *testing.T) {
	sql, _, err := renderFilters(t, []DBFilter{noAccessDBFilter()})
	require.NoError(t, err)
	assert.Contains(t, sql, "1 = 0")
	assert.NotContains(t, sql, "id")
}
