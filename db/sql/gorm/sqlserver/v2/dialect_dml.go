package v2

import (
	"errors"
	"fmt"
	"strings"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// The writes. Each renders T-SQL's own statement for what the builder describes, with RETURNING
// as an OUTPUT clause where the statement takes it, and refuses a clause the statement would
// otherwise have ignored: a Limit on an INSERT, or an ORDER BY on an UPDATE, dropped without a
// word, changes which rows are written.

// outputKind says where an OUTPUT clause reads from: the pseudo-table a bare column comes from,
// and the ones the statement has at all.
type outputKind struct {
	statement string
	prefix    string
	allowed   map[string]bool
}

var (
	insertOutput = outputKind{statement: "INSERT", prefix: "INSERTED", allowed: map[string]bool{"INSERTED": true}}
	updateOutput = outputKind{statement: "UPDATE", prefix: "INSERTED", allowed: map[string]bool{"INSERTED": true, "DELETED": true}}
	deleteOutput = outputKind{statement: "DELETE", prefix: "DELETED", allowed: map[string]bool{"DELETED": true}}
	mergeOutput  = outputKind{statement: "MERGE", prefix: "INSERTED", allowed: map[string]bool{"INSERTED": true, "DELETED": true}}
)

// formatOutput renders RETURNING fields as an OUTPUT clause, or "" when there are none.
//
// A column reads the statement's own pseudo-table — INSERTED for an INSERT, an UPDATE and a
// MERGE, DELETED for a DELETE — so Returning("id") means the row as the statement left it, as
// RETURNING does on Postgres. An explicit INSERTED.<column> or DELETED.<column> is honoured
// where the statement has that pseudo-table (an UPDATE's DELETED.<column> is the value before
// it), and "*" is every column. An expression is refused: RETURNING is how the ORM reads keys
// back, and a computed value belongs in a SELECT.
//
// OUTPUT without INTO is refused by the server on a table with an enabled trigger (Msg 334),
// which is what dbCore.TriggerSensitiveReturning tells a caller.
func formatOutput(fields []string, kind outputKind) (string, error) {
	if len(fields) == 0 {
		return "", nil
	}
	items := make([]string, len(fields))
	for i, field := range fields {
		item, err := outputItem(field, kind)
		if err != nil {
			return "", err
		}
		items[i] = item
	}
	return "OUTPUT " + strings.Join(items, ", "), nil
}

func outputItem(field string, kind outputKind) (string, error) {
	if toks := tokens(field); len(toks) == 1 {
		name := toks[0]
		if name == "*" {
			return kind.prefix + ".*", nil
		}
		if isColumnName(name) {
			return kind.prefix + "." + quoteIdentifier(name), nil
		}
		if parts, ok := splitParts(name); ok && len(parts) == 2 {
			pseudo := strings.ToUpper(parts[0])
			if pseudo == "INSERTED" || pseudo == "DELETED" {
				if !kind.allowed[pseudo] {
					return "", unsupported(fmt.Sprintf("%s in the OUTPUT of %s", pseudo, withArticle(kind.statement)),
						fmt.Sprintf("%s has no %s rows; read %s.<column>", withArticle(kind.statement), pseudo, kind.prefix))
				}
				if parts[1] == "*" {
					return pseudo + ".*", nil
				}
				if isColumnName(parts[1]) {
					return pseudo + "." + quoteIdentifier(parts[1]), nil
				}
			}
		}
	}
	return "", unsupported("an expression in RETURNING",
		fmt.Sprintf("%q is not a column; RETURNING becomes OUTPUT %s.<column>, which takes column "+
			"names, INSERTED.<column>, DELETED.<column> and *", field, kind.prefix))
}

// clauseRefusal is one clause a write does not take, and what to do instead.
type clauseRefusal struct {
	present bool
	clause  string
	hint    string
}

// refuseClauses refuses the first clause in refusals that q carries.
func refuseClauses(statement string, refusals []clauseRefusal) error {
	for _, r := range refusals {
		if r.present {
			return unsupported(fmt.Sprintf("%s on %s", r.clause, withArticle(statement)), r.hint)
		}
	}
	return nil
}

func hasSelectList(q *dbCore.Query) bool {
	return q.Select != nil && (len(q.Select.Fields) > 0 || q.Select.Distinct || len(q.Select.DistinctOn) > 0)
}

func hasWhere(q *dbCore.Query) bool { return q.Where != nil && len(q.Where.Conditions) > 0 }

func hasGroupBy(q *dbCore.Query) bool {
	g := q.GroupBy
	return g != nil && (len(g.Fields) > 0 || len(g.Rollup) > 0 || len(g.Cube) > 0 || len(g.Sets) > 0)
}

func hasHaving(q *dbCore.Query) bool { return q.Having != nil && q.Having.Condition != nil }

func hasOrderBy(q *dbCore.Query) bool { return q.OrderBy != nil && len(q.OrderBy.Fields) > 0 }

// dmlTarget renders the table an INSERT, UPDATE, DELETE or MERGE writes to. It takes no alias:
// T-SQL's INSERT INTO, UPDATE and DELETE FROM name the table alone.
func dmlTarget(table, statement string) (string, error) {
	if strings.TrimSpace(table) == "" {
		return "", fmt.Errorf("sqlserver: %s names no table", withArticle(statement))
	}
	ref, aliased, err := tableRef(table)
	if err != nil {
		return "", err
	}
	if aliased {
		return "", fmt.Errorf("sqlserver: the %s target %q carries an alias; T-SQL's %s takes the table alone",
			statement, table, statement)
	}
	return ref, nil
}

// dmlTop renders an UPDATE's or DELETE's Limit as TOP (n).
func dmlTop(limit *int) (string, error) {
	if limit == nil {
		return "", nil
	}
	if *limit < 0 {
		return "", negativeRefusal("Limit", *limit)
	}
	return fmt.Sprintf("TOP (%d)", *limit), nil
}

// formatInsert renders an INSERT:
//
//	[WITH …] INSERT INTO <t> [(<columns>)] [OUTPUT INSERTED.…] {VALUES (?, …), … | <SELECT> | DEFAULT VALUES}
//
// OUTPUT goes before the source, where T-SQL takes it. An INSERT with no columns and no values
// — the ORM's insert of a row whose every column has a default — is DEFAULT VALUES. The VALUES
// of an INSERT … VALUES is limited to 1000 rows (Msg 10738), and every row must be as wide as
// the column list.
// An INSERT … SELECT renders its SELECT as an insert source, whose ORDER BY is allowed — it
// orders the IDENTITY values the rows are given — and whose WITH is hoisted in front of the
// INSERT, the only place T-SQL takes one.
func (d *SQLServerDialect) formatInsert(q *dbCore.Query) (string, []any, error) {
	insert := q.Insert
	const intoSource = "an INSERT has no rows of its own to filter, group, order or page; " +
		"put the clause on the SELECT passed to FromSelect"
	if err := refuseClauses("INSERT", []clauseRefusal{
		{hasSelectList(q), "a SELECT list", intoSource},
		{q.From != nil, "FROM", intoSource},
		{len(q.Joins) > 0, "JOIN", intoSource},
		{hasWhere(q), "WHERE", intoSource},
		{hasGroupBy(q), "GROUP BY", intoSource},
		{hasHaving(q), "HAVING", intoSource},
		{hasOrderBy(q), "ORDER BY", intoSource},
		{q.Limit != nil, "Limit", intoSource},
		{q.Offset != nil, "Offset", intoSource},
		{len(q.Unions) > 0, "UNION", intoSource},
	}); err != nil {
		return "", nil, err
	}

	table, err := dmlTarget(insert.Table, "INSERT")
	if err != nil {
		return "", nil, err
	}
	columns, err := columnNames(insert.Columns, "INSERT column")
	if err != nil {
		return "", nil, err
	}
	if insert.OnConflict != nil {
		return d.formatUpsert(q, table, columns)
	}
	output, err := formatOutput(q.Returning, insertOutput)
	if err != nil {
		return "", nil, err
	}

	var sourceCTEs []*dbCore.CTEClause
	if insert.FromQuery != nil {
		sourceCTEs = insert.FromQuery.CTEs
	}
	with, args, err := d.formatWith(append(append([]*dbCore.CTEClause{}, q.CTEs...), sourceCTEs...))
	if err != nil {
		return "", nil, err
	}

	var parts []string
	if with != "" {
		parts = append(parts, with)
	}
	head := "INSERT INTO " + table
	if len(columns) > 0 {
		head += " (" + strings.Join(columns, ", ") + ")"
	}
	parts = append(parts, head)
	if output != "" {
		parts = append(parts, output)
	}

	switch {
	case insert.FromQuery != nil:
		if len(insert.Values) > 0 {
			return "", nil, errors.New("sqlserver: an INSERT cannot take both VALUES and a SELECT")
		}
		source, sourceArgs, err := d.formatSelect(insert.FromQuery, scopeInsertSource)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, source)
		args = append(args, sourceArgs...)

	case isDefaultValues(insert):
		if len(columns) > 0 {
			return "", nil, fmt.Errorf("sqlserver: the INSERT names %d columns but gives no values", len(columns))
		}
		parts = append(parts, "DEFAULT VALUES")

	default:
		if len(insert.Values) > MaxInsertRows {
			return "", nil, unsupported(fmt.Sprintf("an INSERT of %d rows", len(insert.Values)),
				fmt.Sprintf("the VALUES of an INSERT takes at most %d rows (Msg 10738); insert in batches", MaxInsertRows))
		}
		values, valueArgs, err := valuesList(insert.Values, len(columns))
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, values)
		args = append(args, valueArgs...)
	}

	return strings.Join(parts, " "), args, nil
}

// isDefaultValues reports whether an INSERT gives no values at all: no rows, or the single
// empty row that Columns().Values() leaves.
func isDefaultValues(insert *dbCore.InsertClause) bool {
	return len(insert.Values) == 0 || len(insert.Values) == 1 && len(insert.Values[0]) == 0
}

// columnNames renders a list of names that must each be a single column.
func columnNames(names []string, role string) ([]string, error) {
	quoted := make([]string, len(names))
	for i, name := range names {
		column, err := columnName(name, role)
		if err != nil {
			return nil, err
		}
		quoted[i] = column
	}
	return quoted, nil
}

// valuesList renders VALUES (?, …), (?, …), binding every value once, row by row. width is the
// number of columns, or 0 when the INSERT names none, and then every row must be as wide as the
// first.
//
// It does not count the rows. The 1000-row limit (Msg 10738) is INSERT … VALUES's alone, and
// formatInsert checks it there; the same list used as a derived table, as both upserts use it,
// has no row limit of its own, only the bind-parameter cap FormatQuery applies to every
// statement.
func valuesList(rows [][]any, width int) (string, []any, error) {
	if len(rows) == 0 {
		return "", nil, errors.New("sqlserver: the INSERT has no rows to insert")
	}
	if width == 0 {
		width = len(rows[0])
	}
	if width == 0 {
		return "", nil, errors.New("sqlserver: VALUES rows with no values; DEFAULT VALUES inserts one row only")
	}

	sets := make([]string, len(rows))
	args := make([]any, 0, len(rows)*width)
	placeholders := "(" + strings.TrimSuffix(strings.Repeat("?, ", width), ", ") + ")"
	for i, row := range rows {
		if len(row) != width {
			return "", nil, fmt.Errorf("sqlserver: VALUES row %d has %d values for %d columns", i+1, len(row), width)
		}
		sets[i] = placeholders
		args = append(args, row...)
	}
	return "VALUES " + strings.Join(sets, ", "), args, nil
}

// formatUpsert renders an INSERT with ON CONFLICT, which T-SQL has no clause for. Both forms
// are faithful: they match on the conflict columns the caller named, and nothing else.
//
// DO NOTHING inserts only the rows whose key is not in the table yet:
//
//	INSERT INTO <t> (<cols>) [OUTPUT …] SELECT [src].<col>, … FROM (VALUES …) AS [src] (<cols>)
//	WHERE NOT EXISTS (SELECT 1 FROM <t> AS [tgt] WITH (UPDLOCK, HOLDLOCK) WHERE [tgt].<key> = [src].<key> AND …)
//
// UPDLOCK and HOLDLOCK hold an update lock on every key the probe reads, and a key-range lock
// on the gap where a missing key would go, until the transaction ends, so two sessions
// inserting the same key cannot both find it missing: one inserts it and the other then sees
// it. Each value is bound once. Rows of one statement that share a key are not deduplicated
// against each other, as Postgres's are: the second fails with a duplicate-key error (Msg
// 2627), so a caller deduplicates its rows first.
//
// The locks are also what the form costs. The probe takes its update locks on keys that exist
// as well as on missing ones, in the order the plan reads the rows, so two concurrent
// multi-row statements whose key sets overlap can each hold a key the other wants next, and
// the server ends one of them as a deadlock victim (Msg 1205, which wrapServerError hints
// on). Postgres takes no lock on a key that is already committed. The insert-only MERGE, which
// takes shared range locks, avoids that for keys that exist and deadlocks instead where both
// statements insert the same new keys through a nonclustered unique index, the usual shape of
// an EF table with an IDENTITY key, so it is not used. The locks cover only the keys the probe
// reads when the match can seek an index: a key column of type varchar compared with a Go
// string, which go-mssqldb sends as nvarchar, is converted on the column's side under a SQL_
// collation, and the probe then scans, and locks, the whole index. docs/DIALECTS.md says what
// a caller does about both.
//
// DO UPDATE is a MERGE, with HOLDLOCK for the same reason (Microsoft's documented guard
// against the upsert race):
//
//	MERGE INTO <t> WITH (HOLDLOCK) AS [tgt] USING (VALUES …) AS [src] (<cols>) ON <keys match>
//	WHEN MATCHED THEN UPDATE SET <col> = ?, … WHEN NOT MATCHED THEN INSERT (<cols>) VALUES ([src].<col>, …)
//	[OUTPUT INSERTED.…];
//
// Its args are the source values first, then the SET values in dbCore.SortedKeys order, which
// is the order their placeholders appear in. MERGE must end with ";" (Msg 10713). A source
// with two rows that match one target row is refused by the server (Msg 8672).
//
// Either form reads its rows from an INSERT … SELECT just as well, the SELECT taking the place
// of the VALUES list as a derived table, with its WITH hoisted in front of the statement. The
// conflict columns must be among the inserted ones, since the match compares the values being
// inserted with the table's. A VALUES list here is a derived table, which has no 1000-row
// limit; only the bind-parameter cap bounds it.
func (d *SQLServerDialect) formatUpsert(q *dbCore.Query, table string, columns []string) (string, []any, error) {
	insert := q.Insert
	conflict := insert.OnConflict
	switch conflict.Action {
	case "DO NOTHING", "DO UPDATE":
	case "":
		return "", nil, errors.New("sqlserver: OnConflict requires DoNothing() or DoUpdate(...)")
	default:
		return "", nil, fmt.Errorf("sqlserver: unknown ON CONFLICT action %q", conflict.Action)
	}

	if len(conflict.Columns) == 0 {
		return "", nil, unsupported("ON CONFLICT without conflict columns",
			"T-SQL has no clause that fires on whichever unique constraint a row violates; name the "+
				"columns of the constraint: OnConflict(col, …)")
	}
	matches := make([]string, len(conflict.Columns))
	for i, name := range conflict.Columns {
		if _, err := columnName(name, "conflict column"); err != nil {
			return "", nil, err
		}
		at := columnIndex(insert.Columns, name)
		if at < 0 {
			return "", nil, unsupported("a conflict column that is not inserted",
				fmt.Sprintf("%q is not among the INSERT's columns; the upsert matches rows on the values "+
					"it inserts, so insert every conflict column", name))
		}
		// The match names the column as the INSERT spells it, which is also how the derived
		// table's column list spells it: on a case-sensitive collation [src].[key] would not
		// find a column declared as [Key].
		key := columns[at]
		matches[i] = "[tgt]." + key + " = [src]." + key
	}

	source, sourceArgs, sourceCTEs, err := d.upsertSource(insert, len(columns))
	if err != nil {
		return "", nil, err
	}
	with, args, err := d.formatWith(append(append([]*dbCore.CTEClause{}, q.CTEs...), sourceCTEs...))
	if err != nil {
		return "", nil, err
	}

	columnList := strings.Join(columns, ", ")
	sourceColumns := make([]string, len(columns))
	for i, column := range columns {
		sourceColumns[i] = "[src]." + column
	}
	sourceList := strings.Join(sourceColumns, ", ")
	match := strings.Join(matches, " AND ")

	var parts []string
	if with != "" {
		parts = append(parts, with)
	}

	if conflict.Action == "DO NOTHING" {
		output, err := formatOutput(q.Returning, insertOutput)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, "INSERT INTO "+table+" ("+columnList+")")
		if output != "" {
			parts = append(parts, output)
		}
		parts = append(parts,
			"SELECT "+sourceList+" FROM "+source+" AS [src] ("+columnList+")",
			"WHERE NOT EXISTS (SELECT 1 FROM "+table+" AS [tgt] WITH (UPDLOCK, HOLDLOCK) WHERE "+match+")")
		return strings.Join(parts, " "), append(args, sourceArgs...), nil
	}

	if len(conflict.SetValues) == 0 {
		return "", nil, errors.New("sqlserver: ON CONFLICT DO UPDATE requires at least one assignment")
	}
	output, err := formatOutput(q.Returning, mergeOutput)
	if err != nil {
		return "", nil, err
	}
	args = append(args, sourceArgs...)
	sets := make([]string, 0, len(conflict.SetValues))
	for _, field := range dbCore.SortedKeys(conflict.SetValues) {
		column, err := columnName(field, "DO UPDATE column")
		if err != nil {
			return "", nil, err
		}
		sets = append(sets, column+" = ?")
		args = append(args, conflict.SetValues[field])
	}

	parts = append(parts,
		"MERGE INTO "+table+" WITH (HOLDLOCK) AS [tgt]",
		"USING "+source+" AS [src] ("+columnList+")",
		"ON "+match,
		"WHEN MATCHED THEN UPDATE SET "+strings.Join(sets, ", "),
		"WHEN NOT MATCHED THEN INSERT ("+columnList+") VALUES ("+sourceList+")")
	if output != "" {
		parts = append(parts, output)
	}
	return strings.Join(parts, " ") + ";", args, nil
}

// upsertSource renders the rows an upsert reads as a parenthesised derived table — a VALUES
// list, or the INSERT … SELECT's SELECT — and returns the SELECT's CTEs for the statement to
// hoist.
func (d *SQLServerDialect) upsertSource(insert *dbCore.InsertClause, width int) (string, []any, []*dbCore.CTEClause, error) {
	if insert.FromQuery == nil {
		if isDefaultValues(insert) {
			return "", nil, nil, errors.New("sqlserver: the upsert has no rows to insert")
		}
		values, args, err := valuesList(insert.Values, width)
		if err != nil {
			return "", nil, nil, err
		}
		return "(" + values + ")", args, nil, nil
	}
	if len(insert.Values) > 0 {
		return "", nil, nil, errors.New("sqlserver: an INSERT cannot take both VALUES and a SELECT")
	}
	// The SELECT becomes a derived table, which is a subquery as far as T-SQL is concerned (no
	// ORDER BY without TOP); its WITH moves to the front of the statement.
	source := *insert.FromQuery
	source.CTEs = nil
	sql, args, err := d.formatSelect(&source, scopeSubquery)
	if err != nil {
		return "", nil, nil, err
	}
	return "(" + sql + ")", args, insert.FromQuery.CTEs, nil
}

// columnIndex returns where columns names column, or -1. Names compare the way a
// case-insensitive collation compares them, and a bracketed spelling by the name it stands for.
func columnIndex(columns []string, column string) int {
	for i, c := range columns {
		if strings.EqualFold(unbracket(c), unbracket(column)) {
			return i
		}
	}
	return -1
}

// dmlRefusals are the clauses an UPDATE and a DELETE do not take.
func dmlRefusals(q *dbCore.Query, statement string) []clauseRefusal {
	verb := strings.ToLower(statement)
	joined := fmt.Sprintf("filter the rows with an IN or EXISTS subquery instead; T-SQL's joined "+
		"%s … FROM form is not something the builder renders", statement)
	meaningless := fmt.Sprintf("%s has none; build the rows to %s with a WHERE", withArticle(statement), verb)
	return []clauseRefusal{
		{hasOrderBy(q), "ORDER BY", fmt.Sprintf("T-SQL's %s takes TOP (n) but no ORDER BY; to %s the "+
			"first n rows in an order, %s a CTE: WithCTE(\"c\", <SELECT with OrderBy and Limit>) and "+
			"%s(\"c\")", statement, verb, verb, titleCase(verb))},
		{q.Offset != nil, "Offset", fmt.Sprintf("T-SQL's %s takes no OFFSET; %s a CTE that pages the "+
			"rows: WithCTE(\"c\", <SELECT with OrderBy, Offset and Limit>) and %s(\"c\")", statement, verb, titleCase(verb))},
		{len(q.Joins) > 0, "JOIN", joined},
		{q.From != nil, "FROM", joined},
		{hasSelectList(q), "a SELECT list", meaningless},
		{hasGroupBy(q), "GROUP BY", meaningless},
		{hasHaving(q), "HAVING", meaningless},
		{len(q.Unions) > 0, "UNION", meaningless},
	}
}

// titleCase turns update into Update, the builder method's name.
func titleCase(word string) string {
	return strings.ToUpper(word[:1]) + word[1:]
}

// formatUpdate renders an UPDATE:
//
//	[WITH …] UPDATE [TOP (n)] <t> SET <col> = ?, … [OUTPUT INSERTED.…] [WHERE …]
//
// A Limit is TOP (n), which updates n arbitrary matching rows; T-SQL's UPDATE takes no ORDER
// BY, so one is refused with the CTE form that does what it would mean. The SET list is in
// dbCore.SortedKeys order, so the SQL is the same on every render. The WHERE renders under the
// dialect's condition context, like a SELECT's.
func (d *SQLServerDialect) formatUpdate(q *dbCore.Query) (string, []any, error) {
	if err := refuseClauses("UPDATE", dmlRefusals(q, "UPDATE")); err != nil {
		return "", nil, err
	}
	table, err := dmlTarget(q.Update.Table, "UPDATE")
	if err != nil {
		return "", nil, err
	}
	if len(q.Update.Values) == 0 {
		return "", nil, errors.New("sqlserver: UPDATE requires at least one SET assignment")
	}
	top, err := dmlTop(q.Limit)
	if err != nil {
		return "", nil, err
	}
	output, err := formatOutput(q.Returning, updateOutput)
	if err != nil {
		return "", nil, err
	}
	with, args, err := d.formatWith(q.CTEs)
	if err != nil {
		return "", nil, err
	}

	sets := make([]string, 0, len(q.Update.Values))
	for _, field := range dbCore.SortedKeys(q.Update.Values) {
		toks := tokens(field)
		if len(toks) != 1 || !isIdentifier(toks[0]) {
			return "", nil, fmt.Errorf("sqlserver: the SET target %q is not a column name", field)
		}
		sets = append(sets, quoteIdentifier(toks[0])+" = ?")
		args = append(args, q.Update.Values[field])
	}

	where, whereArgs, err := d.renderFilter(q.Where, "UPDATE")
	if err != nil {
		return "", nil, err
	}

	var parts []string
	if with != "" {
		parts = append(parts, with)
	}
	parts = append(parts, "UPDATE")
	if top != "" {
		parts = append(parts, top)
	}
	parts = append(parts, table, "SET", strings.Join(sets, ", "))
	if output != "" {
		parts = append(parts, output)
	}
	if where != "" {
		parts = append(parts, "WHERE", where)
		args = append(args, whereArgs...)
	}
	return strings.Join(parts, " "), args, nil
}

// formatDelete renders a DELETE:
//
//	[WITH …] DELETE [TOP (n)] FROM <t> [OUTPUT DELETED.…] [WHERE …]
//
// with the same rules for TOP, ORDER BY and the WHERE as formatUpdate.
func (d *SQLServerDialect) formatDelete(q *dbCore.Query) (string, []any, error) {
	if err := refuseClauses("DELETE", dmlRefusals(q, "DELETE")); err != nil {
		return "", nil, err
	}
	table, err := dmlTarget(q.Delete.Table, "DELETE")
	if err != nil {
		return "", nil, err
	}
	top, err := dmlTop(q.Limit)
	if err != nil {
		return "", nil, err
	}
	output, err := formatOutput(q.Returning, deleteOutput)
	if err != nil {
		return "", nil, err
	}
	with, args, err := d.formatWith(q.CTEs)
	if err != nil {
		return "", nil, err
	}
	where, whereArgs, err := d.renderFilter(q.Where, "DELETE")
	if err != nil {
		return "", nil, err
	}

	var parts []string
	if with != "" {
		parts = append(parts, with)
	}
	parts = append(parts, "DELETE")
	if top != "" {
		parts = append(parts, top)
	}
	parts = append(parts, "FROM", table)
	if output != "" {
		parts = append(parts, output)
	}
	if where != "" {
		parts = append(parts, "WHERE", where)
		args = append(args, whereArgs...)
	}
	return strings.Join(parts, " "), args, nil
}
