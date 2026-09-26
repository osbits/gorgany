package core

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// BinaryCondition represents a condition with two operands and an operator
type BinaryCondition struct {
	Left     interface{}
	Operator string
	Right    interface{}
}

// ToSQL returns the SQL representation of the binary condition. It is ToSQLContext(nil).
func (c *BinaryCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the binary condition under ctx (see RenderContext). A nil ctx runs the
// code below the dispatch, which is what ToSQL has always run.
func (c *BinaryCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	var args []interface{}
	var leftSQL, rightSQL string

	// The left operand sits in an identifier position: a column name is emitted, anything
	// else is bound. See identifierOperandSQL.
	leftSQL, args, _ = identifierOperandSQL(c.Left, args, nil, slotComparisonLeft)

	// The right operand sits in a value position, so a bare string is a value — which is
	// correct and is what every caller wants. Raw and Identifier are how a caller says it
	// means a column there instead; without them a join comparing two columns bound the
	// second one as a string and compared the first against its name.
	switch v := c.Right.(type) {
	case Raw:
		rightSQL = string(v)
	case Identifier:
		rightSQL, args, _ = identifierOperandSQL(v, args, nil, slotComparisonRight)
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		rightSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		rightSQL = "?"
		args = append(args, v)
	}

	return fmt.Sprintf("%s %s %s", leftSQL, c.Operator, rightSQL), args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx. The operator goes through ctx.Operator (see
// RenderContext.operator), and an Identifier on the right is an identifier slot like the left:
// under Strict a demoted one is refused rather than compared against as text.
func (c *BinaryCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}

	leftSQL, args, err := identifierOperandSQL(c.Left, nil, ctx, slotComparisonLeft)
	if err != nil {
		return "", nil, err
	}
	operator, err := ctx.operator(c.Operator, portableComparisons)
	if err != nil {
		return "", nil, err
	}

	var rightSQL string
	switch v := c.Right.(type) {
	case Raw:
		rightSQL = string(v)
	case Identifier:
		rightSQL, args, err = identifierOperandSQL(v, args, ctx, slotComparisonRight)
	case *Query:
		rightSQL, args, err = subqueryOperandSQL(v, args, ctx)
	default:
		rightSQL, args = "?", append(args, v)
	}
	if err != nil {
		return "", nil, err
	}

	return fmt.Sprintf("%s %s %s", leftSQL, operator, rightSQL), args, nil
}

// UnaryCondition represents a condition with one operand and an operator
type UnaryCondition struct {
	Operator string
	Operand  interface{}
}

// ToSQL returns the SQL representation of the unary condition. It is ToSQLContext(nil).
func (c *UnaryCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the unary condition under ctx (see RenderContext). A nil ctx runs the
// code below the dispatch, which is what ToSQL has always run.
func (c *UnaryCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	var args []interface{}
	var operandSQL string

	operandSQL, args, _ = identifierOperandSQL(c.Operand, args, nil, unaryOperandSlot(c.Operator))

	return fmt.Sprintf("%s %s", c.Operator, operandSQL), args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx; the operator goes through ctx.Operator (see
// RenderContext.operator).
func (c *UnaryCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}

	operandSQL, args, err := identifierOperandSQL(c.Operand, nil, ctx, unaryOperandSlot(c.Operator))
	if err != nil {
		return "", nil, err
	}
	operator, err := ctx.operator(c.Operator, portableUnaryOperators)
	if err != nil {
		return "", nil, err
	}

	return fmt.Sprintf("%s %s", operator, operandSQL), args, nil
}

// InCondition represents an IN condition
type InCondition struct {
	Field      interface{}
	Values     []interface{}
	Not        bool
	IsSubquery bool
	Subquery   *Query
}

// ToSQL returns the SQL representation of the IN condition. It is ToSQLContext(nil).
func (c *InCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the IN condition under ctx (see RenderContext). A nil ctx runs the code
// below the dispatch, which is what ToSQL has always run.
func (c *InCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	var args []interface{}
	var fieldSQL string

	// The field sits in an identifier position. See identifierOperandSQL.
	fieldSQL, args, _ = identifierOperandSQL(c.Field, args, nil, slotInField)

	operator := "IN"
	if c.Not {
		operator = "NOT IN"
	}

	if c.IsSubquery {
		sql, subArgs := buildSubquerySQL(c.Subquery)
		return fmt.Sprintf("%s %s (%s)", fieldSQL, operator, sql), append(args, subArgs...), nil
	}

	placeholders := make([]string, len(c.Values))
	for i := range c.Values {
		placeholders[i] = "?"
		args = append(args, c.Values[i])
	}

	return fmt.Sprintf("%s %s (%s)", fieldSQL, operator, strings.Join(placeholders, ", ")), args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx.
func (c *InCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}

	fieldSQL, args, err := identifierOperandSQL(c.Field, nil, ctx, slotInField)
	if err != nil {
		return "", nil, err
	}

	operator := "IN"
	if c.Not {
		operator = "NOT IN"
	}

	if c.IsSubquery {
		sql, subArgs, err := ctx.subquery(c.Subquery)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%s %s (%s)", fieldSQL, operator, sql), append(args, subArgs...), nil
	}

	// An empty list renders as "f IN ()", a syntax error on Postgres, MySQL and SQL Server
	// alike. The context says what it means instead, and the field goes with it: it was
	// rendered above all the same, so a Strict context still refuses one that is not an
	// identifier.
	if len(c.Values) == 0 && ctx.EmptyIn != nil {
		return ctx.EmptyIn(c.Not), nil, nil
	}

	placeholders := make([]string, len(c.Values))
	for i := range c.Values {
		placeholders[i] = "?"
		args = append(args, c.Values[i])
	}

	return fmt.Sprintf("%s %s (%s)", fieldSQL, operator, strings.Join(placeholders, ", ")), args, nil
}

// BetweenCondition represents a BETWEEN condition
type BetweenCondition struct {
	Field interface{}
	Lower interface{}
	Upper interface{}
	Not   bool
}

// ToSQL returns the SQL representation of the BETWEEN condition. It is ToSQLContext(nil).
func (c *BetweenCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the BETWEEN condition under ctx (see RenderContext). A nil ctx runs
// the code below the dispatch, which is what ToSQL has always run.
func (c *BetweenCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	var args []interface{}
	var fieldSQL string

	// The field sits in an identifier position. See identifierOperandSQL.
	fieldSQL, args, _ = identifierOperandSQL(c.Field, args, nil, slotBetweenField)

	operator := "BETWEEN"
	if c.Not {
		operator = "NOT BETWEEN"
	}

	lowerSQL, args := betweenBoundSQL(c.Lower, args)
	upperSQL, args := betweenBoundSQL(c.Upper, args)

	return fmt.Sprintf("%s %s %s AND %s", fieldSQL, operator, lowerSQL, upperSQL), args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx. The bounds follow betweenBoundSQL's rule —
// a subquery renders, anything else binds — with the subquery going through the context.
func (c *BetweenCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}

	fieldSQL, args, err := identifierOperandSQL(c.Field, nil, ctx, slotBetweenField)
	if err != nil {
		return "", nil, err
	}

	operator := "BETWEEN"
	if c.Not {
		operator = "NOT BETWEEN"
	}

	lowerSQL, args, err := valueOperandSQL(c.Lower, args, ctx)
	if err != nil {
		return "", nil, err
	}
	upperSQL, args, err := valueOperandSQL(c.Upper, args, ctx)
	if err != nil {
		return "", nil, err
	}

	return fmt.Sprintf("%s %s %s AND %s", fieldSQL, operator, lowerSQL, upperSQL), args, nil
}

// betweenBoundSQL renders one BETWEEN bound, binding it as a parameter.
//
// A string used to be interpolated straight into the SQL here — `BETWEEN 18 AND 65`
// with no args — which made BetweenCondition the only member of this family to treat a
// string in a *value* position as SQL rather than as a value:
//
//	BinaryCondition{Left: "age", Operator: ">", Right: "18"}   → age > ?        args=[18]
//	InCondition{Field: "age", Values: []any{"18"}}             → age IN (?)     args=[18]
//	LikeCondition{Field: "name", Pattern: "a%"}                → name LIKE ?    args=[a%]
//	BetweenCondition{Field: "age", Lower: "18", Upper: "65"}   → age BETWEEN 18 AND 65
//
// Builder.Between is public API passing its arguments straight through, so an app
// filtering on a date range from the query string produced
// `created_at BETWEEN 1 OR 1=1 -- AND 2` — SQL injection through the framework's own
// builder. It was also a regression for anyone migrating off the removed
// postgres/v2.BetweenCondition, which always parameterised both bounds.
//
// A *Query still renders as a subquery, matching every sibling. To put an *identifier*
// in a bound — `BETWEEN start_col AND end_col` — use a RawCondition, which is the same
// answer the family already gives for BinaryCondition.Right.
func betweenBoundSQL(bound interface{}, args []interface{}) (string, []interface{}) {
	if query, ok := bound.(*Query); ok {
		sql, subArgs := buildSubquerySQL(query)
		return fmt.Sprintf("(%s)", sql), append(args, subArgs...)
	}

	return "?", append(args, bound)
}

// ExistsCondition represents an EXISTS condition
type ExistsCondition struct {
	Query *Query
	Not   bool
}

// ToSQL returns the SQL representation of the EXISTS condition. It is ToSQLContext(nil).
func (c *ExistsCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the EXISTS condition under ctx (see RenderContext). A nil ctx runs the
// code below the dispatch, which is what ToSQL has always run.
func (c *ExistsCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	sql, args := buildSubquerySQL(c.Query)
	operator := "EXISTS"
	if c.Not {
		operator = "NOT EXISTS"
	}
	return fmt.Sprintf("%s (%s)", operator, sql), args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx; the subquery goes through the context.
func (c *ExistsCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}

	sql, args, err := ctx.subquery(c.Query)
	if err != nil {
		return "", nil, err
	}
	operator := "EXISTS"
	if c.Not {
		operator = "NOT EXISTS"
	}
	return fmt.Sprintf("%s (%s)", operator, sql), args, nil
}

// LikeCondition represents a LIKE condition
type LikeCondition struct {
	Field   interface{}
	Pattern interface{}
	Not     bool
	Escape  string
}

// ToSQL returns the SQL representation of the LIKE condition. It is ToSQLContext(nil).
func (c *LikeCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the LIKE condition under ctx (see RenderContext). A nil ctx runs the
// code below the dispatch, which is what ToSQL has always run.
func (c *LikeCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	var args []interface{}
	var fieldSQL string

	// The field sits in an identifier position. See identifierOperandSQL.
	fieldSQL, args, _ = identifierOperandSQL(c.Field, args, nil, slotLikeField)

	operator := "LIKE"
	if c.Not {
		operator = "NOT LIKE"
	}

	// Handle pattern
	var patternSQL string
	switch v := c.Pattern.(type) {
	case string:
		patternSQL = "?"
		args = append(args, v)
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		patternSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		patternSQL = "?"
		args = append(args, v)
	}

	if c.Escape != "" {
		return fmt.Sprintf("%s %s %s ESCAPE '%s'", fieldSQL, operator, patternSQL, c.Escape), args, nil
	}
	return fmt.Sprintf("%s %s %s", fieldSQL, operator, patternSQL), args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx. It refuses an Escape that cannot be written
// as ESCAPE '<escape>' (see isLikeEscape) rather than emitting it.
func (c *LikeCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}
	if c.Escape != "" && !isLikeEscape(c.Escape) {
		return "", nil, Unsupported(ctx.dialectName(), fmt.Sprintf("LIKE ESCAPE %q", c.Escape),
			"the escape must be exactly one character other than a single quote")
	}

	fieldSQL, args, err := identifierOperandSQL(c.Field, nil, ctx, slotLikeField)
	if err != nil {
		return "", nil, err
	}

	operator := "LIKE"
	if c.Not {
		operator = "NOT LIKE"
	}

	patternSQL, args, err := valueOperandSQL(c.Pattern, args, ctx)
	if err != nil {
		return "", nil, err
	}

	if c.Escape != "" {
		return fmt.Sprintf("%s %s %s ESCAPE '%s'", fieldSQL, operator, patternSQL, c.Escape), args, nil
	}
	return fmt.Sprintf("%s %s %s", fieldSQL, operator, patternSQL), args, nil
}

// isLikeEscape reports whether escape is exactly one valid character and not a single quote,
// which is what every engine asks of ESCAPE '<escape>'.
//
// ToSQL interpolates Escape into a string literal as it stands, so a single quote closes the
// literal early and whatever follows it is SQL; and an escape of more than one character is
// refused by every engine, but only once the statement reaches the server. Under a context
// both are caught while the query is being built instead.
//
// It is not the whole of what makes an escape safe on every engine. Where a string literal
// treats a backslash as an escape of its own — MySQL, unless sql_mode has
// NO_BACKSLASH_ESCAPES — ESCAPE '\' escapes the closing quote and leaves the literal open, so
// an engine like that needs more than this check before it renders LIKE under a context.
// T-SQL and standard SQL take '\' as the one-character literal it looks like.
func isLikeEscape(escape string) bool {
	return utf8.ValidString(escape) && utf8.RuneCountInString(escape) == 1 && escape != "'"
}

// IsNullCondition represents an IS NULL condition
type IsNullCondition struct {
	Field interface{}
	Not   bool
}

// ToSQL returns the SQL representation of the IS NULL condition. It is ToSQLContext(nil).
func (c *IsNullCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the IS NULL condition under ctx (see RenderContext). A nil ctx runs the
// code below the dispatch, which is what ToSQL has always run.
func (c *IsNullCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	var args []interface{}
	var fieldSQL string

	// The field sits in an identifier position. See identifierOperandSQL.
	fieldSQL, args, _ = identifierOperandSQL(c.Field, args, nil, slotIsNullField)

	operator := "IS NULL"
	if c.Not {
		operator = "IS NOT NULL"
	}

	return fmt.Sprintf("%s %s", fieldSQL, operator), args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx.
func (c *IsNullCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}

	fieldSQL, args, err := identifierOperandSQL(c.Field, nil, ctx, slotIsNullField)
	if err != nil {
		return "", nil, err
	}

	operator := "IS NULL"
	if c.Not {
		operator = "IS NOT NULL"
	}

	return fmt.Sprintf("%s %s", fieldSQL, operator), args, nil
}

// RawCondition represents a raw SQL condition
type RawCondition struct {
	SQL  string
	Args []interface{}
}

// ToSQL returns the SQL representation of the raw condition
// It supports identifier placeholders used across the project to prevent
// identifiers (table/column) from being bound as string values:
//   - "?.id"  -> consumes 1 arg (table/alias), renders as <arg>.id
//   - "?.?"   -> consumes 2 args (table/alias, column), renders as <arg0>.<arg1>
//
// Remaining "?" placeholders are treated as value parameters and returned in args.
//
// It is ToSQLContext(nil), and like every render it leaves the receiver alone, so one
// RawCondition renders the same however many times it is rendered. It did not always:
// expansion consumed the receiver's Args as it went (c.Args = c.Args[1:]), which made a
// RawCondition single-use. Rendering a builder twice, or rendering two builders cloned from
// one another — clones share their conditions — found the identifier args already gone
// and substituted the next ones in their place; and a placeholder that ran short of args
// fell back to the original SQL with args that were already half consumed. The fast path
// handed out the receiver's own Args slice, so a caller appending to what it got back could
// write into the condition.
func (c *RawCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the raw condition under ctx (see RenderContext). ctx.Raw rewrites the
// SQL first; each identifier a "?." placeholder substitutes then goes through
// ctx.QuoteIdentifier (a core.Raw arg is emitted as it is), and so does the literal column
// after "?." when the context accepts it as an identifier (see placeholderColumn). Under
// Strict a substituted identifier or literal column the context does not accept, and a
// placeholder with no arg left for it, are errors. A nil ctx renders what ToSQL always has,
// less the defects described there.
func (c *RawCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, nil
	}

	sql := c.SQL
	identifier := func(arg any) (string, error) { return fmt.Sprint(arg), nil }
	column := func(literal string) (string, error) { return literal, nil }
	if ctx != nil {
		sql = ctx.rawSQL(sql)
		identifier = ctx.placeholderIdentifier
		column = ctx.placeholderColumn
	}

	// Fast path: if there is no "?." pattern, keep legacy behavior — with a copy of Args, not
	// the receiver's own slice.
	if !strings.Contains(sql, "?.") {
		return sql, cloneArgs(c.Args), nil
	}

	expanded, args, ok, err := expandIdentifierPlaceholders(sql, c.Args, identifier, column)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		// Not enough args; fall back to legacy behavior: the SQL as written, every arg as
		// given. A Strict context refuses instead, since the "?." left in the SQL would be
		// sent to the server as a value placeholder followed by ".<column>".
		if ctx != nil && ctx.Strict {
			return "", nil, fmt.Errorf("RawCondition %q runs out of args at a \"?.\" placeholder: "+
				"\"?.<column>\" takes one arg for the table or alias and \"?.?\" takes two, in order "+
				"with the args of the \"?\" value placeholders", c.SQL)
		}
		return sql, cloneArgs(c.Args), nil
	}
	return expanded, args, nil
}

// expandIdentifierPlaceholders replaces the "?." identifier placeholders in sql, rendering
// each substituted identifier with identifier and each literal column after "?." with column,
// and collects the args left for the "?" value placeholders. It reads args through a local
// cursor and never writes to it. ok is false when a placeholder finds too few args left, and
// the caller decides what that means.
func expandIdentifierPlaceholders(sql string, args []any, identifier func(arg any) (string, error), column func(literal string) (string, error)) (string, []any, bool, error) {
	rest := args
	values := make([]interface{}, 0, len(args))

	// We'll build the final SQL by replacing identifier placeholders and
	// collecting remaining args for value placeholders.
	var sb strings.Builder
	runes := []rune(sql)
	for i := 0; i < len(runes); {
		// Detect "?" placeholder
		if runes[i] == '?' {
			// Check if it's an identifier placeholder followed by "."
			if i+1 < len(runes) && runes[i+1] == '.' {
				// Two forms are supported:
				// 1) "?.?" -> table/alias and column both dynamic (consume 2 args)
				// 2) "?.<word>" -> table/alias dynamic, column literal (consume 1 arg)
				if i+2 < len(runes) && runes[i+2] == '?' { // pattern "?.?"
					// Consume two args for identifiers
					if len(rest) < 2 {
						return "", nil, false, nil
					}
					tbl, err := identifier(rest[0])
					if err != nil {
						return "", nil, false, err
					}
					col, err := identifier(rest[1])
					if err != nil {
						return "", nil, false, err
					}
					rest = rest[2:]
					sb.WriteString(tbl)
					sb.WriteRune('.')
					sb.WriteString(col)
					// Skip "?.?"
					i += 3
					continue
				}

				// pattern "?.<word>"
				// Extract the literal column name following the dot
				j := i + 2 // start after "?."
				for j < len(runes) {
					r := runes[j]
					if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '"' {
						j++
						continue
					}
					break
				}
				if len(rest) < 1 {
					return "", nil, false, nil
				}
				tbl, err := identifier(rest[0])
				if err != nil {
					return "", nil, false, err
				}
				col, err := column(string(runes[i+2 : j]))
				if err != nil {
					return "", nil, false, err
				}
				rest = rest[1:]
				sb.WriteString(tbl)
				sb.WriteRune('.')
				sb.WriteString(col)
				i = j
				continue
			}

			// Value placeholder "?" -> keep as placeholder and collect next arg
			sb.WriteRune('?')
			if len(rest) > 0 {
				values = append(values, rest[0])
				rest = rest[1:]
			}
			i++
			continue
		}

		// Regular character
		sb.WriteRune(runes[i])
		i++
	}

	// Append any leftover args (shouldn't normally happen unless there were more
	// args than placeholders). Keep them to avoid silent loss.
	if len(rest) > 0 {
		values = append(values, rest...)
	}

	return sb.String(), values, true, nil
}

// cloneArgs copies args, keeping a nil slice nil and an empty one empty: a caller can tell the
// two apart, and the goldens do.
func cloneArgs(args []any) []any {
	if args == nil {
		return nil
	}
	return append(make([]any, 0, len(args)), args...)
}

// buildSubquerySQL builds SQL for a subquery and returns its SQL and arguments
func buildSubquerySQL(query *Query) (string, []interface{}) {
	var sqlBuilder strings.Builder
	var args []interface{}

	// Add SELECT clause
	if query.Select != nil {
		if query.Select.Distinct {
			if len(query.Select.DistinctOn) > 0 {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT ON (%s) %s",
					strings.Join(query.Select.DistinctOn, ", "),
					strings.Join(query.Select.Fields, ", ")))
			} else {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT %s",
					strings.Join(query.Select.Fields, ", ")))
			}
		} else {
			sqlBuilder.WriteString(fmt.Sprintf("SELECT %s",
				strings.Join(query.Select.Fields, ", ")))
		}
	}

	// Add FROM clause
	if query.From != nil {
		sqlBuilder.WriteString(" FROM ")
		if query.From.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquerySQL(query.From.Subquery)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, query.From.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(query.From.Table)
		}
	}

	// Add JOINs
	for _, join := range query.Joins {
		sqlBuilder.WriteString(fmt.Sprintf(" %s JOIN ", join.Type))
		if join.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquerySQL(join.Subquery)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, join.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(join.Table)
		}

		if join.Condition != nil {
			conditionSQL, conditionArgs := join.Condition.ToSQL()
			sqlBuilder.WriteString(fmt.Sprintf(" ON %s", conditionSQL))
			args = append(args, conditionArgs...)
		}
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL, whereArgs := query.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(whereSQL)
			args = append(args, whereArgs...)
		}
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" GROUP BY %s",
			strings.Join(query.GroupBy.Fields, ", ")))
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL, havingArgs := query.Having.ToSQL()
		if havingSQL != "" {
			sqlBuilder.WriteString(" HAVING ")
			sqlBuilder.WriteString(havingSQL)
			args = append(args, havingArgs...)
		}
	}

	// Add ORDER BY clause
	if query.OrderBy != nil {
		sqlBuilder.WriteString(" ORDER BY ")
		orderByParts := make([]string, len(query.OrderBy.Fields))
		for i, field := range query.OrderBy.Fields {
			if field.Direction != "" {
				orderByParts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
			} else {
				orderByParts[i] = field.Field
			}
		}
		sqlBuilder.WriteString(strings.Join(orderByParts, ", "))
	}

	// Add LIMIT clause
	if query.Limit != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" LIMIT %d", *query.Limit))
	}

	// Add OFFSET clause
	if query.Offset != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" OFFSET %d", *query.Offset))
	}

	return sqlBuilder.String(), args
}

// CompositeCondition joins conditions with a single boolean operator, parenthesising the
// result when there is more than one.
//
// It lives here with the rest of the condition set, and it did not always: an identical
// type sat in db/sql/gorm/postgres/v2 alongside a set of duplicates of the conditions
// above, which have since been deleted. Nothing engine-specific was ever in them — no
// quoting, `?` placeholders throughout — so they were only there because the whole
// condition family predated the split of the builder out of the Postgres package (T2.1)
// and this file was where the rest of it landed.
//
// The consequence was not cosmetic. model/pagination.go used the Postgres copy, which put
// gorm.io/driver/postgres on the dependency path of every package that imports model — so
// making the engines opt-in (F7) removed MySQL from a Postgres-only app's binary but could
// not remove Postgres from a MySQL-only app's.
type CompositeCondition struct {
	// Operator joins the conditions — "AND" or "OR".
	Operator string
	// Conditions are the operands. One is rendered bare; several are parenthesised.
	Conditions []Condition
}

// ToSQL returns the SQL representation of the composite condition. It is ToSQLContext(nil).
func (c *CompositeCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

// ToSQLContext renders the composite condition under ctx (see RenderContext). A nil ctx runs
// the code below the dispatch, which is what ToSQL has always run.
func (c *CompositeCondition) ToSQLContext(ctx *RenderContext) (string, []any, error) {
	if ctx != nil {
		return c.contextSQL(ctx)
	}

	if c == nil || len(c.Conditions) == 0 {
		return "", nil, nil
	}

	var args []interface{}
	conditions := make([]string, len(c.Conditions))

	for i, condition := range c.Conditions {
		sql, conditionArgs := condition.ToSQL()
		conditions[i] = sql
		args = append(args, conditionArgs...)
	}

	joined := strings.Join(conditions, " "+c.Operator+" ")

	// A single condition needs no parentheses, and adding them would change nothing but
	// the SQL a test has to match.
	if len(c.Conditions) == 1 {
		return joined, args, nil
	}

	return "(" + joined + ")", args, nil
}

// contextSQL is ToSQLContext under a non-nil ctx. Each operand goes through RenderCondition,
// so an app-defined one is validated under Strict, and one that renders empty is skipped
// rather than leaving "(a = ? AND )" behind; the parentheses follow the operands that are
// left. The operator is resolved as RenderWhere resolves a WHERE clause's.
func (c *CompositeCondition) contextSQL(ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, nil
	}

	operator, err := ctx.booleanOperator(c.Operator)
	if err != nil {
		return "", nil, err
	}
	parts, args, err := renderConditionParts(c.Conditions, ctx)
	if err != nil {
		return "", nil, err
	}

	switch len(parts) {
	case 0:
		return "", nil, nil
	case 1:
		return parts[0], args, nil
	}
	return "(" + strings.Join(parts, " "+operator+" ") + ")", args, nil
}
