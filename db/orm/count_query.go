package orm

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// countAlias names the derived table a wrapped count reads from, and countOne the column
// its inner query selects when the caller's projection is replaced. Every engine needs the
// derived table named, and SQL Server needs every column of one named too (Msg 8155), so
// neither may be left anonymous.
const (
	countAlias = "gorgany_count"
	countOne   = "1 AS gorgany_one"
)

// countQuery derives the query that counts the rows src returns, without touching src.
//
// src is what the caller's builder built, and it is shared with that builder: Build returns
// the builder's own query, not a copy. So src is copied shallowly and every clause that
// differs is replaced in the copy, never written through.
//
// What changes, and why:
//   - ORDER BY, LIMIT and OFFSET go. The count is of every row the query matches — the total
//     a paginated list shows — so a page's LIMIT must not cap it, and ORDER BY in a counting
//     query is at best wasted work and on SQL Server an error (Msg 8127, and Msg 1033 inside
//     a derived table).
//   - The select list is replaced, never appended to. CountByQuery used to call
//     Select("COUNT(*)"), which appends, so a builder that had selected columns counted with
//     `SELECT a, b, COUNT(*)` — a grouping error on Postgres, one row per a,b elsewhere.
//   - The caller's FROM is kept. It used to be replaced with the model's table, which
//     dropped a FROM the caller had chosen, such as a subquery or another table. The model's
//     table is used only when the builder has no FROM.
//   - A GROUP BY, HAVING, DISTINCT or UNION query returns one row per group, per distinct
//     row or per row of the union, so replacing its select list with COUNT(*) would count
//     something else. Those are wrapped instead, as SELECT COUNT(*) FROM (<query>) AS
//     gorgany_count. A DISTINCT or UNION query's columns are what make rows distinct, so
//     they are kept, and they must be ones a derived table can carry: see
//     wrappableProjection. A grouped query's columns are kept when a derived table can
//     carry them too, because a GROUP BY may name a column by its alias or its position and
//     a HAVING may name an alias (MySQL). Otherwise — no select list, a star, or a column
//     with no name, such as a bare COUNT(*) — they are replaced with 1 AS gorgany_one, since
//     the number of groups does not depend on them. A GROUP BY that names a column by alias
//     or position cannot survive that replacement, so it is refused: see
//     groupByReferencesSelectList.
//   - Common table expressions move to the outer query, since a WITH cannot open a derived
//     table on every engine.
func countQuery(src *dbCore.Query, table string) (*dbCore.Query, error) {
	if src == nil {
		src = &dbCore.Query{}
	}
	if src.Insert != nil || src.Update != nil || src.Delete != nil {
		return nil, errors.New("orm: CountByQuery counts the rows of a SELECT, but the builder holds an INSERT, UPDATE or DELETE")
	}

	inner := *src
	inner.OrderBy = nil
	inner.Limit = nil
	inner.Offset = nil
	inner.CTEs = nil
	inner.Returning = nil
	if inner.From == nil && table != "" {
		inner.From = &dbCore.FromClause{Table: table}
	}

	hasJoins := len(src.Joins) > 0
	switch {
	case isDistinctSelect(src.Select) || len(src.Unions) > 0:
		if problems := wrappableProjection(src.Select, hasJoins); len(problems) > 0 {
			return nil, fmt.Errorf("orm: CountByQuery cannot wrap a DISTINCT/UNION query with unnamed or "+
				"duplicate columns (%s); alias them", strings.Join(problems, "; "))
		}
		return wrapCount(&inner, src.CTEs), nil

	case isGrouped(src):
		problems := groupedProjection(src.Select, hasJoins)
		if len(problems) == 0 {
			return wrapCount(&inner, src.CTEs), nil
		}
		if reference := groupByReferencesSelectList(src); reference != "" {
			return nil, fmt.Errorf("orm: CountByQuery cannot wrap a grouped query whose GROUP BY "+
				"refers to its select list (%s) while that list cannot be kept (%s); alias its columns",
				reference, strings.Join(problems, "; "))
		}
		inner.Select = &dbCore.SelectClause{Fields: []string{countOne}}
		return wrapCount(&inner, src.CTEs), nil

	default:
		inner.Select = &dbCore.SelectClause{Fields: []string{"COUNT(*)"}}
		inner.CTEs = src.CTEs
		return &inner, nil
	}
}

// wrapCount returns SELECT COUNT(*) FROM (inner) AS gorgany_count, carrying ctes.
func wrapCount(inner *dbCore.Query, ctes []*dbCore.CTEClause) *dbCore.Query {
	return &dbCore.Query{
		CTEs:   ctes,
		Select: &dbCore.SelectClause{Fields: []string{"COUNT(*)"}},
		From:   &dbCore.FromClause{Subquery: inner, Alias: countAlias, IsSubquery: true},
	}
}

// isGrouped reports whether q returns one row per group rather than one per matched row.
// A HAVING without a GROUP BY makes the whole table one group.
func isGrouped(q *dbCore.Query) bool {
	if q.Having != nil && q.Having.Condition != nil {
		return true
	}
	g := q.GroupBy
	return g != nil && len(g.Fields)+len(g.Sets)+len(g.Rollup)+len(g.Cube) > 0
}

// distinctPrefix matches a DISTINCT written into the first select item, as in
// Select("DISTINCT email") or Select("DISTINCT(email)"). The builder has no method that sets
// SelectClause.Distinct, so that is how a caller asks for one, and counting the query without
// noticing it would count duplicates. A word boundary, not whitespace, ends the keyword: the
// parenthesised spelling used to go unnoticed and was counted as every row.
var distinctPrefix = regexp.MustCompile(`(?is)^\s*DISTINCT\b\s*`)

// isDistinctSelect reports whether sel returns distinct rows.
func isDistinctSelect(sel *dbCore.SelectClause) bool {
	if sel == nil {
		return false
	}
	if sel.Distinct || len(sel.DistinctOn) > 0 {
		return true
	}
	return len(sel.Fields) > 0 && distinctPrefix.MatchString(sel.Fields[0])
}

// identifierSegment is one part of a plain or dotted identifier: a bare name, or one quoted
// the way Postgres, MySQL or SQL Server quote them.
const identifierSegment = `(?:[\p{L}_][\p{L}\p{N}_$]*|"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|\[(?:[^\]]|\]\])+\])`

var (
	identifierItem = regexp.MustCompile(`^` + identifierSegment + `(?:\.` + identifierSegment + `)*$`)
	starItem       = regexp.MustCompile(`^(?:` + identifierSegment + `\.)*\*$`)
	aliasedItem    = regexp.MustCompile(`(?is)^(.*\S)\s+AS\s+(` + identifierSegment + `)$`)
	lastSegment    = regexp.MustCompile(identifierSegment + `$`)
)

// wrappableProjection lists why sel's columns cannot be the columns of a derived table, or
// returns nil when they can.
//
// A derived table needs every column named, and named once. SQL Server refuses a column
// without a name (Msg 8155) and a repeated one (Msg 8156), and MySQL refuses a repeated one
// (error 1060). gorgany cannot name an expression for the caller without changing what the
// query means, so it accepts what names itself — a plain or dotted identifier, whose name is
// its last part, or `expr AS alias` — and compares the names case-insensitively, as MySQL
// does. A star names whatever the table has, which is unique only while there is one table:
// it is accepted without a JOIN, and only on its own, since `*, a` repeats a.
func wrappableProjection(sel *dbCore.SelectClause, hasJoins bool) []string {
	items := selectItems(sel)

	var problems []string
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if starItem.MatchString(item) {
			if hasJoins {
				problems = append(problems, fmt.Sprintf("%s with a JOIN", item))
			}
			if len(items) > 1 {
				problems = append(problems, fmt.Sprintf("%s beside other columns", item))
			}
			continue
		}

		name := outputName(item)
		if name == "" {
			problems = append(problems, fmt.Sprintf("%s has no name", item))
			continue
		}
		folded := strings.ToLower(name)
		if seen[folded] {
			problems = append(problems, fmt.Sprintf("%s is not unique", name))
			continue
		}
		seen[folded] = true
	}
	return problems
}

// selectItems returns sel's select-list items as they render, one per column, with a
// DISTINCT written into the first one removed. No select list renders as a star, so it is
// returned as one.
func selectItems(sel *dbCore.SelectClause) []string {
	if sel == nil || len(sel.Fields) == 0 {
		return []string{"*"}
	}
	var items []string
	for i, field := range sel.Fields {
		if i == 0 {
			field = distinctPrefix.ReplaceAllString(field, "")
		}
		items = append(items, splitTopLevel(field)...)
	}
	return items
}

// groupedProjection lists why a grouped query's own columns cannot be kept inside the
// derived table that counts it, or returns nil when they can.
//
// It is wrappableProjection with one difference: a star is never kept. Under GROUP BY a star
// selects columns the query does not group by, which Postgres and MySQL's
// ONLY_FULL_GROUP_BY both refuse, and it names no alias a GROUP BY or HAVING could refer to,
// so replacing it loses nothing.
func groupedProjection(sel *dbCore.SelectClause, hasJoins bool) []string {
	var problems []string
	for _, item := range selectItems(sel) {
		if starItem.MatchString(item) {
			problems = append(problems, fmt.Sprintf("%s is not a named column", item))
		}
	}
	if len(problems) > 0 {
		return problems
	}
	return wrappableProjection(sel, hasJoins)
}

// ordinalItem matches a GROUP BY item that names a select-list column by its position.
var ordinalItem = regexp.MustCompile(`^\d+$`)

// groupByReferencesSelectList returns the first GROUP BY item of q that names a select-list
// column by its position or its alias, or "" when there is none.
//
// Such an item means something only beside the select list it refers to. When countQuery
// replaces that list with 1 AS gorgany_one, a position groups by the constant, which counts
// one group whatever the data holds, and an alias names a column that is no longer there,
// which both engines reject. Only an item that is the whole alias or a whole number counts:
// anything else is an expression over the table's columns, which the replacement does not
// touch.
func groupByReferencesSelectList(q *dbCore.Query) string {
	aliases := make(map[string]bool)
	for _, item := range selectItems(q.Select) {
		if match := aliasedItem.FindStringSubmatch(item); match != nil {
			aliases[strings.ToLower(unquoteSegment(match[2]))] = true
		}
	}

	g := q.GroupBy
	if g == nil {
		return ""
	}
	items := append(append(append([]string{}, g.Fields...), g.Rollup...), g.Cube...)
	for _, set := range g.Sets {
		items = append(items, set...)
	}
	for _, item := range items {
		for _, part := range splitTopLevel(item) {
			if ordinalItem.MatchString(part) || aliases[strings.ToLower(unquoteSegment(part))] {
				return part
			}
		}
	}
	return ""
}

// parenthesisedIdentifier matches one pair of parentheses around a plain or dotted
// identifier, as the item DISTINCT(email) leaves once its DISTINCT is removed.
var parenthesisedIdentifier = regexp.MustCompile(`^\(\s*(` + identifierSegment + `(?:\.` + identifierSegment + `)*)\s*\)$`)

// outputName returns the name item gives its column, unquoted, or "" when it gives none
// gorgany can be sure of.
//
// A column in one pair of parentheses is named by the column, which is what Postgres and
// MySQL call it (MySQL also counts `(email), email` as a repeated name), so
// Select("DISTINCT(email)") is counted rather than refused.
func outputName(item string) string {
	if match := aliasedItem.FindStringSubmatch(item); match != nil {
		return unquoteSegment(match[2])
	}
	if match := parenthesisedIdentifier.FindStringSubmatch(item); match != nil {
		item = match[1]
	}
	if identifierItem.MatchString(item) {
		return unquoteSegment(lastSegment.FindString(item))
	}
	return ""
}

// unquoteSegment strips one identifier segment's quotes and undoubles the quote inside.
func unquoteSegment(segment string) string {
	if len(segment) < 2 {
		return segment
	}
	switch first, last := segment[0], segment[len(segment)-1]; {
	case first == '"' && last == '"':
		return strings.ReplaceAll(segment[1:len(segment)-1], `""`, `"`)
	case first == '`' && last == '`':
		return strings.ReplaceAll(segment[1:len(segment)-1], "``", "`")
	case first == '[' && last == ']':
		return strings.ReplaceAll(segment[1:len(segment)-1], "]]", "]")
	}
	return segment
}

// splitTopLevel splits a select-list string on the commas that separate items, not those
// inside parentheses or quotes, so Select("id, name") is read as the two columns it renders.
func splitTopLevel(list string) []string {
	var items []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(list); i++ {
		c := list[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '[':
			quote = ']'
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ',' && depth == 0:
			items = append(items, strings.TrimSpace(list[start:i]))
			start = i + 1
		}
	}
	return append(items, strings.TrimSpace(list[start:]))
}
