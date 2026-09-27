package v2

import (
	"fmt"
	"regexp"
	"strings"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// Conditions render themselves (core.Condition.ToSQL takes no dialect), which is fine for
// Postgres and MySQL, whose identifiers are bare, and not for T-SQL: a digit-leading or
// reserved name in a WHERE has to be bracketed, and nothing the dialect controls would ever
// see it. So every predicate this dialect emits — WHERE, HAVING, JOIN ON, and the WHERE of an
// UPDATE or DELETE, which the Postgres and MySQL dialects do not check at all — goes through
// core.RenderWhere, core.RenderHaving or core.RenderCondition under conditionContext, which
// hands the condition family this dialect's rules. See core.RenderContext.

// ilikeRE matches ILIKE as a word, so a column called notilike is left alone.
var ilikeRE = regexp.MustCompile(`(?i)\bILIKE\b`)

// rewriteILIKE turns the ILIKE operator in a RawCondition's SQL into LIKE, and leaves every
// other ILIKE alone: one inside a '…' string literal, a [bracketed] identifier or a "quoted"
// identifier, in each of which the closing character doubled stands for itself.
// Those are data and names, and rewriting them would change which rows the condition matches,
// or name a column that does not exist, without an error. Text after a literal or identifier
// that is never closed is left as it is; the server refuses the statement anyway.
func rewriteILIKE(sql string) string {
	if !ilikeRE.MatchString(sql) {
		return sql
	}
	var out strings.Builder
	start := 0 // where the text still to be rewritten begins
	for i := 0; i < len(sql); i++ {
		closer := byte(0)
		switch sql[i] {
		case '\'':
			closer = '\''
		case '[':
			closer = ']'
		case '"':
			closer = '"'
		default:
			continue
		}
		out.WriteString(ilikeRE.ReplaceAllString(sql[start:i], "LIKE"))
		end := delimitedEnd(sql, i+1, closer)
		out.WriteString(sql[i:end])
		start, i = end, end-1
	}
	out.WriteString(ilikeRE.ReplaceAllString(sql[start:], "LIKE"))
	return out.String()
}

// delimitedEnd returns the index just past the closer that ends the literal or identifier
// whose content starts at from, reading a doubled closer as one character of the content, or
// len(sql) when nothing closes it.
func delimitedEnd(sql string, from int, closer byte) int {
	for i := from; i < len(sql); i++ {
		if sql[i] != closer {
			continue
		}
		if i+1 < len(sql) && sql[i+1] == closer {
			i++
			continue
		}
		return i + 1
	}
	return len(sql)
}

// conditionContext is the context every condition renders under.
//
// It is Strict: anything in an identifier slot that is not a name T-SQL can bracket is
// refused rather than bound as a value and compared as text, an app-defined condition that
// predates the seam is validated before it renders, and an operator this dialect does not
// know is refused. A predicate here filters an UPDATE or a DELETE as often as a SELECT, and a
// filter that silently compares a constant matches every row or none.
//
// ILIKE becomes LIKE, both as an operator and inside a RawCondition's SQL, where only the
// operator is rewritten and never text inside a literal or a delimited name (see
// rewriteILIKE). LIKE on SQL Server
// is as case-sensitive as the column's collation, and the default collations, EF's included,
// are case-insensitive; on a _CS_ or binary collation the rewrite compares case-sensitively.
//
// An empty IN list, which is a syntax error written out, is 1=0, and an empty NOT IN is 1=1:
// T-SQL has no boolean literals to write false and true with.
//
// Subqueries nested in a condition render through formatSelect in the subquery scope, so
// they are quoted, checked and paginated as T-SQL needs, not by core's LIMIT/OFFSET
// fallback.
func (d *SQLServerDialect) conditionContext() *dbCore.RenderContext {
	return &dbCore.RenderContext{
		Dialect:         DialectName,
		QuoteIdentifier: quoteIdentifier,
		IsIdentifier:    isIdentifier,
		Subquery: func(q *dbCore.Query) (string, []any, error) {
			return d.formatSelect(q, scopeSubquery)
		},
		Operator: tsqlOperator,
		EmptyIn:  emptyIn,
		Raw:      rewriteILIKE,
		Strict:   true,
	}
}

func emptyIn(not bool) string {
	if not {
		return "1=1"
	}
	return "1=0"
}

// tsqlOperators are the operators a BinaryCondition or UnaryCondition may use, as they are
// emitted. They are the comparisons every engine shares, LIKE and NOT LIKE, the unary NOT,
// EXISTS and NOT EXISTS, and IS [NOT] DISTINCT FROM, which needs SQL Server 2022 or Azure SQL
// and fails at the server on older versions.
var tsqlOperators = map[string]string{
	"=": "=", "<>": "<>", "!=": "!=", "<": "<", "<=": "<=", ">": ">", ">=": ">=",
	"LIKE": "LIKE", "NOT LIKE": "NOT LIKE",
	"ILIKE": "LIKE", "NOT ILIKE": "NOT LIKE",
	"NOT": "NOT", "EXISTS": "EXISTS", "NOT EXISTS": "NOT EXISTS",
	"IS DISTINCT FROM": "IS DISTINCT FROM", "IS NOT DISTINCT FROM": "IS NOT DISTINCT FROM",
}

const (
	regexHint = "SQL Server has no regular-expression operators; use LIKE, whose patterns take " +
		"[a-z] and [^0-9] character classes, or a RawCondition written for SQL Server"
	jsonHint = "that is a Postgres/MySQL JSON operator; use JSON_VALUE(col, '$.path') or OPENJSON " +
		"in a core.Raw operand or a RawCondition"
	containmentHint = "that is a Postgres array or range operator; SQL Server has neither type, so " +
		"compare the columns the value is made of, or use EXISTS over OPENJSON"
)

// refusedOperators are the operators other engines have and SQL Server does not, each with
// what to write instead. Anything else unknown is refused too, with a generic hint: an
// operator is SQL placed next to its operands, so one this dialect does not recognise is
// never emitted.
var refusedOperators = map[string]string{
	"~": regexHint, "~*": regexHint, "!~": regexHint, "!~*": regexHint,
	"SIMILAR TO": regexHint, "NOT SIMILAR TO": regexHint,
	"REGEXP": regexHint, "NOT REGEXP": regexHint, "RLIKE": regexHint, "NOT RLIKE": regexHint,
	"@>": containmentHint, "<@": containmentHint, "&&": containmentHint,
	"?": jsonHint, "?|": jsonHint, "?&": jsonHint,
	"->": jsonHint, "->>": jsonHint, "#>": jsonHint, "#>>": jsonHint,
	"@@":  "that is Postgres full-text search; use CONTAINS(col, ?) or FREETEXT(col, ?) in a RawCondition",
	"<=>": "that is MySQL's NULL-safe equality; use IS NOT DISTINCT FROM (SQL Server 2022, Azure SQL)",
}

// tsqlOperator is the context's Operator hook: it maps an operator to its T-SQL spelling, or
// refuses it. Case and spacing do not matter, so the framework's own "like" and "not like"
// get through, emitted as LIKE and NOT LIKE.
func tsqlOperator(op string) (string, error) {
	normalized := strings.ToUpper(strings.Join(strings.Fields(op), " "))
	if spelled, ok := tsqlOperators[normalized]; ok {
		return spelled, nil
	}
	if hint, ok := refusedOperators[normalized]; ok {
		return "", unsupported("the "+normalized+" operator", hint)
	}
	return "", unsupported(fmt.Sprintf("the operator %q", op),
		"a condition may compare with =, <>, !=, <, <=, >, >=, LIKE, NOT LIKE or IS [NOT] DISTINCT FROM; "+
			"write anything else as a RawCondition")
}

// renderWhere renders a WHERE clause's predicate without the keyword; "" means there is none.
func (d *SQLServerDialect) renderWhere(where *dbCore.WhereClause) (string, []any, error) {
	if where == nil || len(where.Conditions) == 0 {
		return "", nil, nil
	}
	sql, args, err := dbCore.RenderWhere(where, d.conditionContext())
	if err != nil {
		return "", nil, fmt.Errorf("cannot render WHERE: %w", err)
	}
	return sql, args, nil
}

// renderFilter renders the WHERE of an UPDATE or a DELETE. A WHERE whose conditions all render
// empty would leave the statement unfiltered — every row, where the caller asked for some — so
// it is refused, as core refuses a nil condition for the same reason. A query with no
// conditions at all is an unfiltered statement on purpose, and renders as one.
func (d *SQLServerDialect) renderFilter(where *dbCore.WhereClause, statement string) (string, []any, error) {
	sql, args, err := d.renderWhere(where)
	if err != nil {
		return "", nil, err
	}
	if sql == "" && where != nil && len(where.Conditions) > 0 {
		return "", nil, fmt.Errorf("sqlserver: every condition of the %s's WHERE rendered empty, "+
			"which would %s every row of the table; refusing it", statement, strings.ToLower(statement))
	}
	return sql, args, nil
}

// renderJoinOn renders a JOIN's ON condition. One that renders empty is refused: T-SQL has no
// JOIN without ON other than CROSS JOIN, and dropping the condition would make it one.
func (d *SQLServerDialect) renderJoinOn(condition dbCore.Condition) (string, []any, error) {
	sql, args, err := dbCore.RenderCondition(condition, d.conditionContext())
	if err != nil {
		return "", nil, fmt.Errorf("cannot render JOIN ON: %w", err)
	}
	if strings.TrimSpace(sql) == "" {
		return "", nil, fmt.Errorf("sqlserver: the JOIN's ON condition renders empty; give it a condition, or use CrossJoin")
	}
	return sql, args, nil
}

// isTrivialOn reports whether a LATERAL join's condition is one that says nothing — none at
// all, or a RawCondition of true or 1=1 without args — which is what a LATERAL join is written
// with on Postgres (LEFT JOIN LATERAL (…) AS x ON true). T-SQL's APPLY takes no ON at all, so
// only such a condition can be dropped when LATERAL becomes APPLY.
func isTrivialOn(condition dbCore.Condition) bool {
	if condition == nil {
		return true
	}
	raw, ok := condition.(*dbCore.RawCondition)
	if !ok {
		return false
	}
	if raw == nil {
		return true
	}
	if len(raw.Args) > 0 {
		return false
	}
	switch strings.ToUpper(strings.Join(strings.Fields(raw.SQL), "")) {
	case "", "TRUE", "1=1":
		return true
	}
	return false
}
