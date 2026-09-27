// Package v2 implements the SQL Server and Azure SQL Database SQL dialect.
//
// Target: SQL Server 2016 or newer and Azure SQL Database. The dialect emits nothing newer than
// SQL Server 2012 (OFFSET … FETCH) on its own; IS [NOT] DISTINCT FROM, which a caller may use as
// a condition operator, needs SQL Server 2022 or Azure SQL.
//
// The governing rule is the MySQL dialect's: every construct T-SQL cannot express returns an
// explicit dbCore.UnsupportedError rather than SQL the server will reject, and nothing is
// translated into SQL that runs but means something else. SQL Server adds a second concern
// Postgres and MySQL do not have: the schemas it is pointed at are often ones EF Core owns,
// with tables such as [dbo].[2024Orders] and [Order] that are only valid delimited. So every
// identifier the dialect emits is bracketed — in the clauses it renders itself and, through
// core.RenderContext, in the conditions — and a string that is not an identifier is either an
// expression emitted as the caller wrote it or, where only a name belongs, refused. See
// identifier.go for what counts as one, and docs/DIALECTS.md for the table of what is
// refused and translated.
//
// Placeholders stay "?", as on the other engines. gorm rewrites them to @p1…@pN when the
// statement runs, so in what it writes itself — names, keywords, a LIKE's ESCAPE — the dialect
// never emits a "?" that is not a placeholder, nor an "@", which would switch gorm to named
// parameters; a bracketed name holding either is refused, as a table or select item too. An
// expression it emits as the caller wrote it — a function in FROM, a computed select item, a
// Raw ORDER BY, a RawCondition — is the caller's SQL, and gorm reads any "?" in it, quoted or
// not, as a placeholder.
package v2

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// DialectName is the registry key and error-message name for this dialect. It is also what
// gorm's SQL Server Dialector reports as its Name(), which is how framework packages that must
// not link the driver tell a SQL Server connection apart.
const DialectName = "sqlserver"

// MaxBindParameters is the most bind parameters one statement may carry. SQL Server takes 2100
// parameters per request, and go-mssqldb sends a parameterised statement through
// sp_executesql, whose @stmt and @params take two of them.
const MaxBindParameters = 2098

// MaxInsertRows is the most rows one VALUES list may hold (Msg 10738).
const MaxInsertRows = 1000

// SQLServerDialect implements dbCore.SQLDialect for SQL Server and Azure SQL Database.
type SQLServerDialect struct {
	// ReadOnly makes FormatQuery refuse every write, before it renders anything, with an error
	// that wraps dbCore.ErrReadOnly and names the statement: an INSERT, including INSERT …
	// SELECT and an upsert, an UPDATE and a DELETE, and a write nested in a CTE, a UNION arm
	// or a subquery. Reads render exactly as they do without it, refusals included, and so
	// does FormatReturning, so dbCore.SupportsReturning gives the same answer either way.
	//
	// The datasource sets it from databases.<name>.read_only, so every builder a read-only
	// session hands out refuses a write where it is built. The guard on the connection
	// refuses the SQL too, but only once it is sent.
	ReadOnly bool
}

var (
	_ dbCore.SQLDialect                = (*SQLServerDialect)(nil)
	_ dbCore.BindParameterLimiter      = (*SQLServerDialect)(nil)
	_ dbCore.TriggerSensitiveReturning = (*SQLServerDialect)(nil)
)

// Name identifies the dialect.
func (d *SQLServerDialect) Name() string { return DialectName }

// MaxBindParameters reports the bind-parameter cap, so a caller that batches work can size
// its batches to fit (see dbCore.BindParameterLimit).
func (d *SQLServerDialect) MaxBindParameters() int { return MaxBindParameters }

// ReturningBlockedByTriggers is true: SQL Server refuses OUTPUT without INTO, which is what
// RETURNING becomes, on a table with an enabled trigger (Msg 334). See
// dbCore.TriggerSensitiveReturning.
func (d *SQLServerDialect) ReturningBlockedByTriggers() bool { return true }

func unsupported(construct, hint string) error {
	return dbCore.Unsupported(DialectName, construct, hint)
}

// selectScope is where a SELECT sits in the statement, which decides what T-SQL lets it
// carry: a WITH only at the top of a statement, ORDER BY in a subquery only with TOP or
// OFFSET, nothing but a bare query in a UNION arm.
type selectScope int

const (
	// scopeTop is the statement itself.
	scopeTop selectScope = iota
	// scopeCTEBody is the query of a WITH entry.
	scopeCTEBody
	// scopeSubquery is a subquery in a condition (IN, EXISTS, a comparison) or a derived table
	// in FROM, JOIN or APPLY.
	scopeSubquery
	// scopeUnionArm is a query after UNION or UNION ALL.
	scopeUnionArm
	// scopeInsertSource is the SELECT of an INSERT … SELECT, whose CTEs the INSERT hoists.
	scopeInsertSource
)

func (s selectScope) String() string {
	switch s {
	case scopeCTEBody:
		return "a CTE"
	case scopeSubquery:
		return "a subquery"
	case scopeUnionArm:
		return "a UNION arm"
	case scopeInsertSource:
		return "an INSERT … SELECT source"
	}
	return "a query"
}

// FormatSelect formats the SELECT clause. An empty field list selects "*".
//
// Each field is rendered by selectItem: "*", t.* and identifiers are bracketed, and
// "<identifier> AS <name>" is bracketed on both sides; anything else is an expression and is
// emitted as written. DISTINCT ON is Postgres-only and is refused rather than degraded to a
// plain DISTINCT, which would return a different row set.
func (d *SQLServerDialect) FormatSelect(fields []string, distinct bool, distinctOn []string) (string, []any, error) {
	return d.selectHead(fields, distinct, distinctOn, "")
}

// selectHead renders SELECT [DISTINCT] [TOP (n)] <list>; top is "" or "TOP (n)". A DISTINCT
// written into the first field is rendered in its place too (see liftDistinct).
func (d *SQLServerDialect) selectHead(fields []string, distinct bool, distinctOn []string, top string) (string, []any, error) {
	if len(distinctOn) > 0 {
		return "", nil, distinctOnRefusal()
	}
	fields, lifted, err := liftDistinct(fields)
	if err != nil {
		return "", nil, err
	}
	distinct = distinct || lifted
	list, err := selectList(fields)
	if err != nil {
		return "", nil, err
	}
	parts := []string{"SELECT"}
	if distinct {
		parts = append(parts, "DISTINCT")
	}
	if top != "" {
		parts = append(parts, top)
	}
	parts = append(parts, list)
	return strings.Join(parts, " "), nil, nil
}

func selectList(fields []string) (string, error) {
	if len(fields) == 0 {
		return "*", nil
	}
	items := make([]string, len(fields))
	for i, field := range fields {
		item, err := selectItem(field)
		if err != nil {
			return "", err
		}
		items[i] = item
	}
	return strings.Join(items, ", "), nil
}

func distinctOnRefusal() error {
	return unsupported("DISTINCT ON",
		"rewrite as a ROW_NUMBER() OVER (PARTITION BY … ORDER BY …) window in a derived table filtered to rn = 1")
}

// FormatFrom formats the FROM clause. The table is rendered by tableRef — t, t a and t AS a are
// bracketed, anything else is emitted as written — and alias, when set, must be a one-part
// name.
func (d *SQLServerDialect) FormatFrom(table string, alias string) (string, []any, error) {
	ref, err := aliasedTableRef(table, alias)
	if err != nil {
		return "", nil, err
	}
	return "FROM " + ref, nil, nil
}

// aliasedTableRef renders a table and an alias given apart, as FromClause and JoinClause hold
// them.
func aliasedTableRef(table, alias string) (string, error) {
	ref, aliased, err := tableRef(table)
	if err != nil {
		return "", err
	}
	if alias == "" {
		return ref, nil
	}
	if aliased {
		return "", fmt.Errorf("sqlserver: the table %q already names an alias, and the alias %q is given as well", table, alias)
	}
	quoted, err := aliasName(alias, "table alias")
	if err != nil {
		return "", err
	}
	return ref + " AS " + quoted, nil
}

// formatFromClause renders a FROM clause, including the derived-table form FormatFrom's
// signature cannot express. T-SQL requires a derived table to be aliased.
func (d *SQLServerDialect) formatFromClause(from *dbCore.FromClause) (string, []any, error) {
	if from == nil {
		return "", nil, nil
	}
	if !from.IsSubquery {
		return d.FormatFrom(from.Table, from.Alias)
	}
	if from.Subquery == nil {
		return "", nil, errors.New("sqlserver: FROM is marked as a subquery but carries no query")
	}
	if from.Alias == "" {
		return "", nil, errors.New("sqlserver: a derived table in FROM requires an alias")
	}
	sql, args, err := d.FormatSubquery(from.Subquery, from.Alias)
	if err != nil {
		return "", nil, err
	}
	return "FROM " + sql, args, nil
}

// joinKinds are the join types T-SQL has, as they are emitted.
var joinKinds = map[string]string{
	"":            "JOIN",
	"INNER":       "INNER JOIN",
	"LEFT":        "LEFT JOIN",
	"LEFT OUTER":  "LEFT OUTER JOIN",
	"RIGHT":       "RIGHT JOIN",
	"RIGHT OUTER": "RIGHT OUTER JOIN",
	"FULL":        "FULL JOIN",
	"FULL OUTER":  "FULL OUTER JOIN",
	"CROSS":       "CROSS JOIN",
}

// applyKinds are what a LATERAL join becomes, keyed by its type once LATERAL is taken out, and
// T-SQL's own APPLY spellings.
var applyKinds = map[string]string{
	"":            "CROSS APPLY",
	"INNER":       "CROSS APPLY",
	"CROSS":       "CROSS APPLY",
	"LEFT":        "OUTER APPLY",
	"LEFT OUTER":  "OUTER APPLY",
	"CROSS APPLY": "CROSS APPLY",
	"OUTER APPLY": "OUTER APPLY",
}

// FormatJoin formats a JOIN clause.
//
// INNER, LEFT, RIGHT, FULL [OUTER] and CROSS are emitted as they are. NATURAL is refused,
// since T-SQL has no join that matches columns by name, and so is any type it does not know.
// A CROSS JOIN with a condition is refused rather than having the condition dropped, and
// every other join needs one.
//
// LATERAL becomes APPLY: LATERAL (…) AS x ON true is CROSS APPLY (…) AS [x], and LEFT LATERAL
// is OUTER APPLY. APPLY has no ON, so only a condition that says nothing (nil, true or 1=1)
// can go; a real one is refused, since the correlation belongs in the subquery's WHERE, where
// APPLY lets it refer to the outer row.
//
// The ON condition renders under the dialect's condition context, like a WHERE: identifiers
// are bracketed and anything that is not one is refused. A derived table must be aliased.
func (d *SQLServerDialect) FormatJoin(join *dbCore.JoinClause) (string, []any, error) {
	if join == nil {
		return "", nil, nil
	}

	kind := strings.ToUpper(strings.Join(strings.Fields(join.Type), " "))
	lateral := join.IsLateral
	if rest, found := cutWord(kind, "LATERAL"); found {
		kind, lateral = rest, true
	}
	if strings.HasSuffix(kind, "APPLY") {
		lateral = true
	}

	if strings.Contains(" "+kind+" ", " NATURAL ") {
		return "", nil, unsupported("NATURAL JOIN",
			"T-SQL has no join that matches columns by name; spell the condition with ON")
	}

	if lateral {
		return d.formatApply(join, kind)
	}

	keyword, ok := joinKinds[kind]
	if !ok {
		return "", nil, unsupported(fmt.Sprintf("a %q join", join.Type),
			"use INNER, LEFT, RIGHT, FULL or CROSS, or LATERAL for APPLY")
	}

	target, args, err := d.joinTarget(join)
	if err != nil {
		return "", nil, err
	}
	sql := keyword + " " + target

	if kind == "CROSS" {
		if join.Condition != nil {
			return "", nil, unsupported("CROSS JOIN with an ON condition",
				"a CROSS JOIN takes no ON; use an INNER JOIN with the condition, or move it to WHERE")
		}
		return sql, args, nil
	}
	if join.Condition == nil {
		return "", nil, fmt.Errorf("sqlserver: %s needs an ON condition; use CrossJoin for a join without one", keyword)
	}
	on, onArgs, err := d.renderJoinOn(join.Condition)
	if err != nil {
		return "", nil, err
	}
	return sql + " ON " + on, append(args, onArgs...), nil
}

// formatApply renders a LATERAL join as CROSS APPLY or OUTER APPLY.
func (d *SQLServerDialect) formatApply(join *dbCore.JoinClause, kind string) (string, []any, error) {
	keyword, ok := applyKinds[kind]
	if !ok {
		return "", nil, unsupported(fmt.Sprintf("a %s LATERAL join", kind),
			"a LATERAL join becomes CROSS APPLY (inner) or OUTER APPLY (left); T-SQL has no right or full form")
	}
	if !isTrivialOn(join.Condition) {
		return "", nil, unsupported("a LATERAL join with an ON condition",
			"LATERAL becomes APPLY, which takes no ON; move the condition into the subquery's WHERE, "+
				"where it can refer to the outer row, and pass the join no condition (or true)")
	}
	target, args, err := d.joinTarget(join)
	if err != nil {
		return "", nil, err
	}
	return keyword + " " + target, args, nil
}

// joinTarget renders what a JOIN or APPLY joins: a table reference, or an aliased derived
// table.
func (d *SQLServerDialect) joinTarget(join *dbCore.JoinClause) (string, []any, error) {
	if !join.IsSubquery {
		ref, err := aliasedTableRef(join.Table, join.Alias)
		return ref, nil, err
	}
	if join.Subquery == nil {
		return "", nil, errors.New("sqlserver: a JOIN is marked as a subquery but carries no query")
	}
	if join.Alias == "" {
		return "", nil, errors.New("sqlserver: a derived table in a JOIN requires an alias")
	}
	return d.FormatSubquery(join.Subquery, join.Alias)
}

// cutWord removes one whole word from a space-separated, upper-case string.
func cutWord(s, word string) (string, bool) {
	fields := strings.Fields(s)
	for i, field := range fields {
		if field == word {
			return strings.Join(append(fields[:i:i], fields[i+1:]...), " "), true
		}
	}
	return s, false
}

// FormatWhere formats the WHERE clause. See conditionContext for how conditions render: every
// identifier is bracketed, ILIKE becomes LIKE, an empty IN is 1=0, and a condition that would
// degrade to comparing text is refused.
func (d *SQLServerDialect) FormatWhere(where *dbCore.WhereClause) (string, []any, error) {
	sql, args, err := d.renderWhere(where)
	if err != nil || sql == "" {
		return "", nil, err
	}
	return "WHERE " + sql, args, nil
}

// normalizeOrderDirection whitelists the sort direction to ASC or DESC, defaulting to ASC for
// anything unrecognized, so a caller cannot inject through the direction slot.
func normalizeOrderDirection(direction string) string {
	if strings.EqualFold(strings.TrimSpace(direction), "desc") {
		return "DESC"
	}
	return "ASC"
}

// orderByField renders one ORDER BY entry. The direction is whitelisted; a Raw field is the
// caller's trusted expression and is emitted verbatim; an identifier is bracketed.
//
// Anything else is refused. The Postgres and MySQL dialects bind it as a value instead, which
// is safe but orders by a constant, so the ORDER BY silently does nothing; and SQL Server
// refuses a constant ORDER BY item outright (Msg 1008), so binding it would only move the
// failure to the server.
func orderByField(f dbCore.OrderByField) (string, error) {
	direction := normalizeOrderDirection(f.Direction)
	if f.Raw {
		return f.Field + " " + direction, nil
	}
	if toks := tokens(f.Field); len(toks) == 1 && isIdentifier(toks[0]) {
		return quoteIdentifier(toks[0]) + " " + direction, nil
	}
	return "", unsupported("ORDER BY on something that is not a column",
		fmt.Sprintf("%q is not a column reference; order by a column name, or pass a trusted "+
			"expression to OrderByRaw", f.Field))
}

func orderByList(fields []dbCore.OrderByField) (string, error) {
	items := make([]string, len(fields))
	for i, field := range fields {
		item, err := orderByField(field)
		if err != nil {
			return "", err
		}
		items[i] = item
	}
	return strings.Join(items, ", "), nil
}

// FormatOrderBy formats the ORDER BY clause for a single field.
func (d *SQLServerDialect) FormatOrderBy(field string, direction string) (string, []any, error) {
	item, err := orderByField(dbCore.OrderByField{Field: field, Direction: direction})
	if err != nil {
		return "", nil, err
	}
	return "ORDER BY " + item, nil, nil
}

// FormatGroupBy formats the GROUP BY clause with ISO grouping sets, as Postgres spells them:
// GROUP BY a, ROLLUP (b, c), CUBE (d), GROUPING SETS ((a), (a, b), ()). T-SQL's older
// WITH ROLLUP / WITH CUBE forms are not used; they take at most ten expressions and cannot be
// combined with plain grouping fields. Identifiers are bracketed; expressions are emitted as
// written; a select-list position is refused (see groupItem).
func (d *SQLServerDialect) FormatGroupBy(groupBy *dbCore.GroupByClause) (string, []any, error) {
	if groupBy == nil {
		return "", nil, nil
	}

	terms := make([]string, 0, 4)
	if len(groupBy.Fields) > 0 {
		list, err := groupList(groupBy.Fields)
		if err != nil {
			return "", nil, err
		}
		terms = append(terms, list)
	}
	for _, modifier := range []struct {
		keyword string
		fields  []string
	}{{"ROLLUP", groupBy.Rollup}, {"CUBE", groupBy.Cube}} {
		if len(modifier.fields) == 0 {
			continue
		}
		list, err := groupList(modifier.fields)
		if err != nil {
			return "", nil, err
		}
		terms = append(terms, modifier.keyword+" ("+list+")")
	}
	if len(groupBy.Sets) > 0 {
		sets := make([]string, len(groupBy.Sets))
		for i, set := range groupBy.Sets {
			list, err := groupList(set)
			if err != nil {
				return "", nil, err
			}
			sets[i] = "(" + list + ")"
		}
		terms = append(terms, "GROUPING SETS ("+strings.Join(sets, ", ")+")")
	}

	if len(terms) == 0 {
		return "", nil, nil
	}
	return "GROUP BY " + strings.Join(terms, ", "), nil, nil
}

func groupList(fields []string) (string, error) {
	items := make([]string, len(fields))
	for i, field := range fields {
		item, err := groupItem(field)
		if err != nil {
			return "", err
		}
		items[i] = item
	}
	return strings.Join(items, ", "), nil
}

// FormatHaving formats the HAVING clause, under the same condition context as WHERE.
func (d *SQLServerDialect) FormatHaving(having *dbCore.HavingClause) (string, []any, error) {
	if having == nil || having.Condition == nil {
		return "", nil, nil
	}
	sql, args, err := dbCore.RenderHaving(having, d.conditionContext())
	if err != nil {
		return "", nil, fmt.Errorf("cannot render HAVING: %w", err)
	}
	if sql == "" {
		return "", nil, nil
	}
	return "HAVING " + sql, args, nil
}

// FormatLimit formats a row limit, which T-SQL spells TOP (n) after SELECT, UPDATE or DELETE.
// A Limit together with an Offset becomes FETCH NEXT instead; formatSelect decides which.
func (d *SQLServerDialect) FormatLimit(limit int) (string, []any, error) {
	if limit < 0 {
		return "", nil, negativeRefusal("Limit", limit)
	}
	return fmt.Sprintf("TOP (%d)", limit), nil, nil
}

// FormatOffset formats an offset, which T-SQL spells OFFSET n ROWS after ORDER BY.
func (d *SQLServerDialect) FormatOffset(offset int) (string, []any, error) {
	if offset < 0 {
		return "", nil, negativeRefusal("Offset", offset)
	}
	return fmt.Sprintf("OFFSET %d ROWS", offset), nil, nil
}

func negativeRefusal(clause string, n int) error {
	return fmt.Errorf("sqlserver: %s(%d) is negative; T-SQL takes no negative row count", clause, n)
}

// FormatCTE formats one WITH entry: [name] AS (<query>). The query may not carry a WITH of its
// own, since T-SQL allows WITH only at the start of a statement, nor an ORDER BY without TOP or
// OFFSET (Msg 1033).
func (d *SQLServerDialect) FormatCTE(name string, query *dbCore.Query) (string, []any, error) {
	quoted, err := aliasName(name, "CTE name")
	if err != nil {
		return "", nil, err
	}
	if query == nil {
		return "", nil, fmt.Errorf("sqlserver: the CTE %q carries no query", name)
	}
	sql, args, err := d.formatSelect(query, scopeCTEBody)
	if err != nil {
		return "", nil, err
	}
	return quoted + " AS (" + sql + ")", args, nil
}

// formatWith renders WITH <cte>, <cte>… or "" when there are none.
func (d *SQLServerDialect) formatWith(ctes []*dbCore.CTEClause) (string, []any, error) {
	if len(ctes) == 0 {
		return "", nil, nil
	}
	parts := make([]string, len(ctes))
	var args []any
	for i, cte := range ctes {
		if cte == nil {
			return "", nil, errors.New("sqlserver: a nil CTE")
		}
		sql, cteArgs, err := d.FormatCTE(cte.Name, cte.Query)
		if err != nil {
			return "", nil, err
		}
		parts[i] = sql
		args = append(args, cteArgs...)
	}
	return "WITH " + strings.Join(parts, ", "), args, nil
}

// FormatUnion formats a UNION clause. The arm must be a bare query — no WITH, ORDER BY, Limit,
// Offset or UNION of its own — since T-SQL lets only the whole UNION be ordered or paged.
func (d *SQLServerDialect) FormatUnion(query *dbCore.Query, all bool) (string, []any, error) {
	if query == nil {
		return "", nil, errors.New("sqlserver: a UNION carries no query")
	}
	sql, args, err := d.formatSelect(query, scopeUnionArm)
	if err != nil {
		return "", nil, err
	}
	if all {
		return "UNION ALL " + sql, args, nil
	}
	return "UNION " + sql, args, nil
}

// FormatWindow formats a named window definition, [name] AS (<definition>), for a WINDOW
// clause (SQL Server 2022 at compatibility level 160, or Azure SQL).
//
// formatSelect does not emit a WINDOW clause from Query.Windows, as the Postgres and MySQL
// dialects do not: the builder's Over(name) inlines the definition into the select item it
// returns, as core's WindowDefinition.String spells it. formatSelect renders each definition
// the query holds with windowDefinition instead, in place of that text (see overWindows), so
// the rules below hold on the builder's path too. They are that PARTITION BY and ORDER BY
// items must be column names, which are bracketed (an ORDER BY item marked Raw is the caller's
// expression and is emitted as written); a frame is ROWS or RANGE, whose bounds are UNBOUNDED
// PRECEDING, n PRECEDING, CURRENT ROW, n FOLLOWING and UNBOUNDED FOLLOWING, with n only under
// ROWS; and GROUPS frames and EXCLUDE are refused, since T-SQL has neither.
func (d *SQLServerDialect) FormatWindow(name string, definition *dbCore.WindowDefinition) (string, []any, error) {
	if definition == nil {
		return "", nil, nil
	}
	quoted, err := aliasName(name, "window name")
	if err != nil {
		return "", nil, err
	}
	body, err := windowDefinition(definition)
	if err != nil {
		return "", nil, err
	}
	return quoted + " AS (" + body + ")", nil, nil
}

// overWindows renders the windows a query defines where the builder's Over(name) placed them.
//
// Over returns name + " OVER (" + WindowDefinition.String() + ")", which a caller puts in the
// select list, and core's text neither brackets a name nor knows what T-SQL refuses: GROUPS,
// EXCLUDE and a RANGE offset were sent, and [Order] as Order. So each definition in windows is
// rendered with windowDefinition, which refuses what T-SQL cannot say, whether or not a select
// item uses it, and every occurrence of the builder's text for it in fields is replaced with
// the T-SQL rendering. The name before OVER, which the builder uses as the function, as in
// ROW_NUMBER(), stays as written.
func overWindows(fields []string, windows []*dbCore.WindowClause) ([]string, error) {
	if len(windows) == 0 {
		return fields, nil
	}
	rendered := append([]string(nil), fields...)
	for _, window := range windows {
		if window == nil || window.Definition == nil {
			continue
		}
		body, err := windowDefinition(window.Definition)
		if err != nil {
			return nil, err
		}
		inlined := window.Name + " OVER (" + window.Definition.String() + ")"
		tsql := window.Name + " OVER (" + body + ")"
		for i, field := range rendered {
			rendered[i] = strings.ReplaceAll(field, inlined, tsql)
		}
	}
	return rendered, nil
}

func windowDefinition(definition *dbCore.WindowDefinition) (string, error) {
	var parts []string

	if len(definition.PartitionBy) > 0 {
		items := make([]string, len(definition.PartitionBy))
		for i, item := range definition.PartitionBy {
			toks := tokens(item)
			if len(toks) != 1 || !isIdentifier(toks[0]) {
				return "", unsupported("a window PARTITION BY item that is not a column",
					fmt.Sprintf("%q is not a column reference; partition by column names", item))
			}
			items[i] = quoteIdentifier(toks[0])
		}
		parts = append(parts, "PARTITION BY "+strings.Join(items, ", "))
	}

	if len(definition.OrderBy) > 0 {
		list, err := orderByList(definition.OrderBy)
		if err != nil {
			return "", err
		}
		parts = append(parts, "ORDER BY "+list)
	}

	if definition.Frame != nil {
		frame, err := windowFrame(definition.Frame)
		if err != nil {
			return "", err
		}
		parts = append(parts, frame)
	}

	return strings.Join(parts, " "), nil
}

func windowFrame(frame *dbCore.WindowFrame) (string, error) {
	kind := strings.ToUpper(strings.TrimSpace(frame.Type))
	switch kind {
	case "ROWS", "RANGE":
	case "GROUPS":
		return "", unsupported("a GROUPS window frame", "T-SQL has ROWS and RANGE frames only")
	default:
		return "", fmt.Errorf("sqlserver: unknown window frame type %q; use ROWS or RANGE", frame.Type)
	}
	if strings.TrimSpace(frame.Exclusion) != "" {
		return "", unsupported("a window frame EXCLUDE clause", "T-SQL has no frame exclusion")
	}
	if frame.Start == nil {
		return "", errors.New("sqlserver: a window frame needs a start bound")
	}
	start, err := frameBound(frame.Start, kind)
	if err != nil {
		return "", err
	}
	if frame.End == nil {
		return kind + " " + start, nil
	}
	end, err := frameBound(frame.End, kind)
	if err != nil {
		return "", err
	}
	return kind + " BETWEEN " + start + " AND " + end, nil
}

// frameBound renders one bound. An offset is emitted as a literal, since T-SQL takes no
// parameter there, so it has to be a non-negative integer.
func frameBound(bound *dbCore.FrameBound, kind string) (string, error) {
	boundType := strings.ToUpper(strings.Join(strings.Fields(bound.Type), " "))
	switch boundType {
	case "UNBOUNDED PRECEDING", "CURRENT ROW", "UNBOUNDED FOLLOWING":
		if bound.Value != nil {
			return "", fmt.Errorf("sqlserver: the window frame bound %s takes no value", boundType)
		}
		return boundType, nil
	case "PRECEDING", "FOLLOWING":
		if kind == "RANGE" {
			return "", unsupported("a RANGE frame with an offset",
				"T-SQL's RANGE takes only UNBOUNDED and CURRENT ROW bounds; use ROWS for an offset")
		}
		n, ok := frameOffset(bound.Value)
		if !ok {
			return "", fmt.Errorf("sqlserver: the window frame offset %#v is not a non-negative integer", bound.Value)
		}
		return fmt.Sprintf("%d %s", n, boundType), nil
	}
	return "", fmt.Errorf("sqlserver: unknown window frame bound %q", bound.Type)
}

func frameOffset(value any) (uint64, bool) {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(v.Int()), v.Int() >= 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint(), true
	}
	return 0, false
}

// FormatSubquery formats a subquery: (<query>) AS [alias], or (<query>) when alias is empty.
// The query is rendered in the subquery scope — no WITH, and no ORDER BY without TOP or
// OFFSET.
func (d *SQLServerDialect) FormatSubquery(query *dbCore.Query, alias string) (string, []any, error) {
	if query == nil {
		return "", nil, errors.New("sqlserver: a subquery carries no query")
	}
	quoted := ""
	if alias != "" {
		var err error
		if quoted, err = aliasName(alias, "subquery alias"); err != nil {
			return "", nil, err
		}
	}
	sql, args, err := d.formatSelect(query, scopeSubquery)
	if err != nil {
		return "", nil, err
	}
	if quoted == "" {
		return "(" + sql + ")", args, nil
	}
	return "(" + sql + ") AS " + quoted, args, nil
}

// FormatDistinctOn always fails: T-SQL has no DISTINCT ON.
func (d *SQLServerDialect) FormatDistinctOn(fields []string) (string, []any, error) {
	return "", nil, distinctOnRefusal()
}

// FormatReturning formats RETURNING as T-SQL's OUTPUT clause for an INSERT: OUTPUT
// INSERTED.[id], …. The statement renderers place OUTPUT where each statement takes it and
// read DELETED for a DELETE; see formatOutput. It renders under ReadOnly as well, so
// dbCore.SupportsReturning reports the dialect's capability, not the datasource's policy.
func (d *SQLServerDialect) FormatReturning(fields []string) (string, []any, error) {
	sql, err := formatOutput(fields, insertOutput)
	if err != nil {
		return "", nil, err
	}
	return sql, nil, nil
}

// FormatQuery renders a complete query.
//
// A write on a ReadOnly dialect is refused before anything renders. A statement with more
// bind parameters than SQL Server takes is refused after rendering, with the count, rather
// than sent to fail at the server.
func (d *SQLServerDialect) FormatQuery(q *dbCore.Query) (string, []any, error) {
	if q == nil {
		return "", nil, errors.New("sqlserver: cannot format a nil query")
	}
	statement, err := statementKind(q)
	if err != nil {
		return "", nil, err
	}
	if statement != "" && d.ReadOnly {
		return "", nil, readOnlyRefusal(statement)
	}

	var sql string
	var args []any
	switch {
	case q.Insert != nil:
		sql, args, err = d.formatInsert(q)
	case q.Update != nil:
		sql, args, err = d.formatUpdate(q)
	case q.Delete != nil:
		sql, args, err = d.formatDelete(q)
	default:
		sql, args, err = d.formatSelect(q, scopeTop)
	}
	if err != nil {
		return "", nil, err
	}

	if len(args) > MaxBindParameters {
		return "", nil, tooManyParameters(len(args))
	}
	return sql, args, nil
}

// statementKind names the write q is, or "" for a read. A query that is two writes at once is
// an error: which one FormatQuery rendered would be a matter of dispatch order.
func statementKind(q *dbCore.Query) (string, error) {
	var kinds []string
	if q.Insert != nil {
		if q.Insert.OnConflict != nil {
			kinds = append(kinds, "INSERT ... ON CONFLICT")
		} else {
			kinds = append(kinds, "INSERT")
		}
	}
	if q.Update != nil {
		kinds = append(kinds, "UPDATE")
	}
	if q.Delete != nil {
		kinds = append(kinds, "DELETE")
	}
	switch len(kinds) {
	case 0:
		return "", nil
	case 1:
		return kinds[0], nil
	}
	return "", fmt.Errorf("sqlserver: a query cannot be both %s", strings.Join(kinds, " and "))
}

// withArticle puts the indefinite article in front of a statement name.
func withArticle(statement string) string {
	if strings.HasPrefix(statement, "INSERT") || strings.HasPrefix(statement, "UPDATE") {
		return "an " + statement
	}
	return "a " + statement
}

func readOnlyRefusal(statement string) error {
	return fmt.Errorf("%w: %s refuses %s on a read_only datasource", dbCore.ErrReadOnly, DialectName, statement)
}

// pagination is how a SELECT's Limit and Offset render: TOP (n) after SELECT [DISTINCT], or
// OFFSET m ROWS [FETCH NEXT n ROWS ONLY] after an ORDER BY, which is synthesised as
// ORDER BY (SELECT NULL) when the query has none.
type pagination struct {
	top       string
	offset    string
	synthetic bool
}

// paginate decides a SELECT's pagination:
//
//	Limit n, no Offset or Offset 0      TOP (n)
//	Limit 0, any Offset                 TOP (0)
//	Limit n, Offset m > 0               ORDER BY … OFFSET m ROWS FETCH NEXT n ROWS ONLY
//	no Limit, Offset m                  ORDER BY … OFFSET m ROWS
//
// TOP is preferred wherever it says the same thing: it needs no ORDER BY, TOP (0) is legal
// where FETCH NEXT 0 is not, and it makes an ORDER BY legal in a subquery. OFFSET needs an
// ORDER BY, so ORDER BY (SELECT NULL) stands in for a missing one when there is a row to skip —
// the same unspecified order a LIMIT/OFFSET without ORDER BY has on the other engines. An
// Offset(0) with no Limit and no ORDER BY skips nothing and renders nothing.
func paginate(q *dbCore.Query, scope selectScope) (pagination, error) {
	var p pagination
	limit, offset := q.Limit, q.Offset
	if limit != nil && *limit < 0 {
		return p, negativeRefusal("Limit", *limit)
	}
	if offset != nil && *offset < 0 {
		return p, negativeRefusal("Offset", *offset)
	}
	hasOrder := q.OrderBy != nil && len(q.OrderBy.Fields) > 0

	if scope == scopeUnionArm {
		if limit != nil || offset != nil || hasOrder {
			return p, unsupported("ORDER BY, Limit or Offset on a UNION arm",
				"T-SQL orders and pages only a whole UNION; wrap the arm in a derived table "+
					"(Subquery(arm, alias)) and select from that")
		}
		return p, nil
	}

	switch {
	case limit != nil && (offset == nil || *offset == 0 || *limit == 0):
		p.top = fmt.Sprintf("TOP (%d)", *limit)
	case offset != nil && (*offset > 0 || hasOrder):
		p.offset = fmt.Sprintf("OFFSET %d ROWS", *offset)
		if limit != nil {
			p.offset += fmt.Sprintf(" FETCH NEXT %d ROWS ONLY", *limit)
		}
		p.synthetic = !hasOrder
	}

	if hasOrder && p.top == "" && p.offset == "" && (scope == scopeCTEBody || scope == scopeSubquery) {
		return p, unsupported(fmt.Sprintf("ORDER BY in %s without Limit or Offset", scope),
			"SQL Server refuses ORDER BY in a subquery, derived table or CTE unless TOP or OFFSET "+
				"goes with it (Msg 1033); drop the ORDER BY, or add a Limit")
	}
	if p.synthetic && q.Select != nil && (q.Select.Distinct || startsWithDistinct(q.Select.Fields)) {
		return p, unsupported("DISTINCT with an Offset and no ORDER BY",
			"order the query by the selected columns, so the rows skipped are the same on every run")
	}
	return p, nil
}

// formatSelect renders a SELECT in scope, in T-SQL's clause order:
//
//	[WITH …] SELECT [DISTINCT] [TOP (n)] … FROM … JOIN/APPLY … WHERE … GROUP BY … HAVING …
//	[UNION …] [ORDER BY …] [OFFSET m ROWS [FETCH NEXT n ROWS ONLY]]
//
// What a SELECT may carry depends on where it sits:
//
//	                       top      CTE body  subquery  UNION arm  INSERT source
//	its own WITH           emitted  refused   refused   refused    hoisted by the INSERT
//	ORDER BY, no paging    allowed  refused   refused   refused    allowed
//	Limit / Offset         yes      yes       yes       refused    yes
//
// A query with UNIONs may not be ordered or paged itself, in any scope: the builder cannot say
// whether its ORDER BY or Limit was meant for the first arm or the whole UNION. A derived
// table in FROM or a JOIN must be aliased everywhere. Whatever the scope, the query must be a
// SELECT, and RETURNING on it is refused.
func (d *SQLServerDialect) formatSelect(q *dbCore.Query, scope selectScope) (string, []any, error) {
	if q == nil {
		return "", nil, fmt.Errorf("sqlserver: cannot render a nil query as %s", scope)
	}
	statement, err := statementKind(q)
	if err != nil {
		return "", nil, err
	}
	if statement != "" {
		if d.ReadOnly {
			return "", nil, readOnlyRefusal(statement)
		}
		return "", nil, unsupported(fmt.Sprintf("%s as %s", withArticle(statement), scope),
			"a CTE, a subquery and a UNION arm must be a SELECT in T-SQL; run the write as its own statement")
	}
	if len(q.Returning) > 0 {
		return "", nil, unsupported("RETURNING on a SELECT",
			"OUTPUT, which RETURNING becomes, belongs to INSERT, UPDATE, DELETE and MERGE")
	}

	hasOrder := q.OrderBy != nil && len(q.OrderBy.Fields) > 0
	if len(q.Unions) > 0 {
		if scope == scopeUnionArm {
			return "", nil, unsupported("a UNION nested in a UNION arm",
				"list every arm on the outermost query, or wrap the inner UNION in a derived table")
		}
		if hasOrder || q.Limit != nil || q.Offset != nil {
			return "", nil, unsupported("ORDER BY, Limit or Offset on a query with UNION",
				"the builder cannot tell whether they apply to the first arm or to the whole UNION; "+
					"wrap the UNION in a derived table (Subquery(union, alias)) and order or page that")
		}
	}

	var parts []string
	var args []any

	if len(q.CTEs) > 0 {
		switch scope {
		case scopeTop:
			with, withArgs, err := d.formatWith(q.CTEs)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, with)
			args = append(args, withArgs...)
		case scopeInsertSource:
			// Hoisted in front of the INSERT by the statement that holds this source.
		default:
			return "", nil, unsupported(fmt.Sprintf("a WITH clause inside %s", scope),
				"T-SQL allows WITH only at the start of a statement; move the CTE to the outermost query")
		}
	}

	page, err := paginate(q, scope)
	if err != nil {
		return "", nil, err
	}

	var fields, distinctOn []string
	distinct := false
	if q.Select != nil {
		fields, distinct, distinctOn = q.Select.Fields, q.Select.Distinct, q.Select.DistinctOn
	}
	if fields, err = overWindows(fields, q.Windows); err != nil {
		return "", nil, err
	}
	head, _, err := d.selectHead(fields, distinct, distinctOn, page.top)
	if err != nil {
		return "", nil, err
	}
	parts = append(parts, head)

	appendPart := func(sql string, partArgs []any, err error) error {
		if err != nil {
			return err
		}
		if sql != "" {
			parts = append(parts, sql)
			args = append(args, partArgs...)
		}
		return nil
	}

	if err := appendPart(d.formatFromClause(q.From)); err != nil {
		return "", nil, err
	}
	for _, join := range q.Joins {
		if err := appendPart(d.FormatJoin(join)); err != nil {
			return "", nil, err
		}
	}
	if err := appendPart(d.FormatWhere(q.Where)); err != nil {
		return "", nil, err
	}
	if err := appendPart(d.FormatGroupBy(q.GroupBy)); err != nil {
		return "", nil, err
	}
	if err := appendPart(d.FormatHaving(q.Having)); err != nil {
		return "", nil, err
	}
	for _, union := range q.Unions {
		if union == nil {
			return "", nil, errors.New("sqlserver: a nil UNION")
		}
		if err := appendPart(d.FormatUnion(union.Query, union.All)); err != nil {
			return "", nil, err
		}
	}

	switch {
	case hasOrder:
		list, err := orderByList(q.OrderBy.Fields)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "ORDER BY "+list)
	case page.synthetic:
		parts = append(parts, "ORDER BY (SELECT NULL)")
	}
	if page.offset != "" {
		parts = append(parts, page.offset)
	}

	return strings.Join(parts, " "), args, nil
}
