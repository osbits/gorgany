package v2

import (
	"errors"
	"fmt"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTSQLOperatorSpellsWhatSQLServerHas(t *testing.T) {
	tests := map[string]string{
		"=": "=", "<>": "<>", "!=": "!=", "<": "<", "<=": "<=", ">": ">", ">=": ">=",
		"like": "LIKE", "not  like": "NOT LIKE", "ILIKE": "LIKE", "not ilike": "NOT LIKE",
		"NOT": "NOT", "exists": "EXISTS", "NOT EXISTS": "NOT EXISTS",
		"is distinct from": "IS DISTINCT FROM", "IS NOT DISTINCT FROM": "IS NOT DISTINCT FROM",
	}
	for in, want := range tests {
		got, err := tsqlOperator(in)
		require.NoErrorf(t, err, "tsqlOperator(%q)", in)
		assert.Equalf(t, want, got, "tsqlOperator(%q)", in)
	}
}

// TestPgAndMySQLOnlyOperatorsAreRefused: each operator another engine has and SQL Server does
// not is refused with what to write instead, and so is anything unknown, since an operator is
// SQL placed between two operands.
func TestPgAndMySQLOnlyOperatorsAreRefused(t *testing.T) {
	for _, op := range []string{
		"~", "~*", "!~", "!~*", "SIMILAR TO", "not similar to", "REGEXP", "NOT REGEXP", "RLIKE", "NOT RLIKE",
		"@>", "<@", "&&", "?", "?|", "?&", "->", "->>", "#>", "#>>", "@@", "<=>",
	} {
		_, err := tsqlOperator(op)
		requireUnsupported(t, err, "the "+normalizeForTest(op)+" operator")
		assert.Contains(t, err.Error(), "; ", "a refusal for %q carries a hint", op)
	}

	for _, op := range []string{"= 1 OR 1=1 --", "||", "LIKE BINARY", "", "IN"} {
		_, err := tsqlOperator(op)
		require.Errorf(t, err, "tsqlOperator(%q)", op)
		assert.True(t, dbCore.IsUnsupported(err))
	}

	// Through a builder, the refusal names the operator and leaves no SQL behind.
	sql, args, err := NewBuilder().Select("Id").From("t").Where(&dbCore.BinaryCondition{Left: "Tags", Operator: "@>", Right: "x"}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the @> operator")
	assert.Empty(t, sql)
	assert.Nil(t, args)
}

func normalizeForTest(op string) string {
	switch op {
	case "not similar to":
		return "NOT SIMILAR TO"
	}
	return op
}

func TestRawConditionPlaceholdersAreBracketed(t *testing.T) {
	sql, args := render(t, NewBuilder().Select("r.*").From("dbo.Role r").
		InnerJoin("dbo.UserRoles", &dbCore.RawCondition{SQL: "?.Id = ?.?", Args: []any{"dbo.Role", "dbo.UserRoles", "RoleId"}}).
		Where(&dbCore.RawCondition{SQL: "?.Order > ?", Args: []any{"r", 1}}))
	assert.Equal(t,
		"SELECT [r].* FROM [dbo].[Role] AS [r] INNER JOIN [dbo].[UserRoles] ON [dbo].[Role].[Id] = [dbo].[UserRoles].[RoleId] "+
			"WHERE [r].[Order] > ?",
		sql)
	assert.Equal(t, []any{1}, args)

	// A substituted name that is not one is refused rather than emitted.
	_, _, err := NewBuilder().Select("Id").From("t").Where(&dbCore.RawCondition{SQL: "?.Id = 1", Args: []any{"t; DROP TABLE x"}}).ToSQL()
	require.Error(t, err)

	// So is a "?." placeholder left without an arg.
	_, _, err = NewBuilder().Select("Id").From("t").Where(&dbCore.RawCondition{SQL: "?.Id = ?.Id", Args: []any{"t"}}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runs out of args")
}

// TestPlaceholderColumnMayBeAnyLetters: the literal column after "?." is read to its end in
// any script, as bareSegment reads a name. Read to the first non-ASCII letter, Größe became
// [Gr]öße, which the server refuses (Msg 4145).
func TestPlaceholderColumnMayBeAnyLetters(t *testing.T) {
	sql, args := render(t, NewBuilder().Select("Id").From("dbo.Maße m").
		Where(&dbCore.RawCondition{SQL: "?.Größe = ? AND ?.Kosten_$ > ?", Args: []any{"m", 7, "m", 1}}))
	assert.Equal(t, "SELECT [Id] FROM [dbo].[Maße] AS [m] WHERE [m].[Größe] = ? AND [m].[Kosten_$] > ?", sql)
	assert.Equal(t, []any{7, 1}, args)

	// Without a context the column is emitted as written, wherever the scan ends.
	raw, rawArgs := (&dbCore.RawCondition{SQL: "?.Größe = ?", Args: []any{"m", 7}}).ToSQL()
	assert.Equal(t, "m.Größe = ?", raw)
	assert.Equal(t, []any{7}, rawArgs)
}

// TestLikeEscapeMustBeOneCharacter: under a context, core refuses an ESCAPE that cannot be
// written as ESCAPE '<c>'.
func TestLikeEscapeMustBeOneCharacter(t *testing.T) {
	_, _, err := NewBuilder().Select("Id").From("t").LikeEscape("Name", "a", "'; DROP TABLE t --").ToSQL()
	requireUnsupported(t, err, `LIKE ESCAPE "'; DROP TABLE t --"`)

	sql, args := render(t, NewBuilder().Select("Id").From("t").NotLikeEscape("Name", `100\%`, `\`))
	assert.Equal(t, `SELECT [Id] FROM [t] WHERE [Name] NOT LIKE ? ESCAPE '\'`, sql)
	assert.Equal(t, []any{`100\%`}, args)

	// gorm reads "?" in the statement as a placeholder, literal or not, and "@" as the start of
	// a named parameter, so neither can be the escape.
	for _, escape := range []string{"?", "@"} {
		_, _, err = NewBuilder().Select("Id").From("t").LikeEscape("Note", "x??y", escape).Eq("Order", 4).ToSQL()
		requireUnsupported(t, err, fmt.Sprintf("LIKE ESCAPE %q", escape))
		assert.Contains(t, err.Error(), "such as '!'")
	}
}

func TestIsTrivialOn(t *testing.T) {
	var nilRaw *dbCore.RawCondition
	for _, c := range []dbCore.Condition{nil, nilRaw, &dbCore.RawCondition{}, &dbCore.RawCondition{SQL: "true"},
		&dbCore.RawCondition{SQL: " True "}, &dbCore.RawCondition{SQL: "1=1"}, &dbCore.RawCondition{SQL: "1 = 1"}} {
		assert.Truef(t, isTrivialOn(c), "%#v is trivial", c)
	}
	for _, c := range []dbCore.Condition{
		&dbCore.RawCondition{SQL: "a = b"},
		&dbCore.RawCondition{SQL: "true", Args: []any{1}},
		&dbCore.RawCondition{SQL: "1=2"},
		&dbCore.BinaryCondition{Left: dbCore.Raw("1"), Operator: "=", Right: 1},
	} {
		assert.Falsef(t, isTrivialOn(c), "%#v is a real condition", c)
	}
}

// TestAConditionThatPredatesTheSeamIsValidated: an app-defined condition with no ToSQLContext
// is validated before its ToSQL runs, since the context is Strict.
func TestAConditionThatPredatesTheSeamIsValidated(t *testing.T) {
	sql, args := render(t, NewBuilder().Select("Id").From("t").Where(legacyCondition{}))
	assert.Equal(t, "SELECT [Id] FROM [t] WHERE tenant_id = ?", sql)
	assert.Equal(t, []any{7}, args)

	_, _, err := NewBuilder().Select("Id").From("t").Where(legacyCondition{refuse: true}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy refusal")
}

type legacyCondition struct{ refuse bool }

func (legacyCondition) ToSQL() (string, []any) { return "tenant_id = ?", []any{7} }

func (c legacyCondition) Validate() error {
	if c.refuse {
		return errors.New("legacy refusal")
	}
	return nil
}
