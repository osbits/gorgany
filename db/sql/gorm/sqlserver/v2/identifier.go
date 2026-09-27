package v2

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// What counts as an identifier here is wider than core.IsSimpleIdentifier, and it has to be.
// EF Core names tables after the entity class, so a schema it owns has names such as 2024Orders
// (a digit first), Order and User (reserved words), and Order Lines (a space) once a
// [Table] attribute is involved. Postgres and MySQL leave identifiers bare, so a digit-leading
// name is not one there; this dialect delimits every identifier it emits, so it can be. The
// recogniser below decides which strings the dialect may bracket, and everything it accepts is
// emitted through QuoteIdentifier, which brackets each part.
//
// A string it does not accept is not an identifier: in a SELECT item or a table reference it is
// an expression the caller wrote and is emitted verbatim, and in a position that must hold a
// name — an INSERT column, a SET target, an alias, a CTE name, an ORDER BY field — it is
// refused.

// bareSegment is one unbracketed part of a name: letters, digits, "_" and "$", with at least
// one letter or "_", which may come after leading digits (2024Orders). T-SQL's own rule for a
// regular identifier wants a letter first; this one also takes the digit-leading names EF
// produces, which are only valid bracketed — and this dialect brackets them.
var bareSegment = regexp.MustCompile(`^\p{N}*[\p{L}_][\p{L}\p{N}_$]*$`)

// A segment bareSegment accepts can still be a number: 1e5 and 2E10 are float literals, 0x1F
// and a bare 0x are binary ones. Bracketing one would turn a constant into a column reference,
// so they are not identifiers. All-digit segments never match bareSegment in the first place.
var (
	floatLiteral  = regexp.MustCompile(`^[0-9]+[eE][+-]?[0-9]+$`)
	binaryLiteral = regexp.MustCompile(`^0[xX][0-9A-Fa-f]*$`)
)

// niladicKeywords are T-SQL's functions that take no parentheses, and NULL. Each looks like a
// name and is not one: [CURRENT_TIMESTAMP] is a column of that name, and there is none. So none
// of them is ever bracketed, and in a slot that must hold a column they are refused rather than
// turned into one. USER is niladic as well and is left out on purpose: User is one of the most
// common EF table names, and it has to be bracketed to be read as one.
var niladicKeywords = map[string]bool{
	"CURRENT_TIMESTAMP": true,
	"CURRENT_USER":      true,
	"SESSION_USER":      true,
	"SYSTEM_USER":       true,
	"CURRENT_DATE":      true,
	"NULL":              true,
}

// maxIdentifierParts is the most parts a T-SQL name has: server.database.schema.object.
const maxIdentifierParts = 4

// isIdentifier reports whether s is a name of one to four dot-separated parts, each of them
// either a bare segment (see bareSegment) or a bracketed one, and nothing else.
//
// A bracketed part may hold anything a bracket can — spaces, dots, a "]" doubled as "]]" —
// except "@" and "?". gorm reads every "?" in a statement as a placeholder, bracketed or not,
// and a statement that contains "@" switches gorm to named parameters, so either one inside a
// name the dialect emits would misplace the arguments bound after it.
func isIdentifier(s string) bool {
	parts, ok := splitParts(s)
	if !ok || len(parts) == 0 || len(parts) > maxIdentifierParts {
		return false
	}
	if len(parts) == 1 && niladicKeywords[strings.ToUpper(parts[0])] {
		return false
	}
	for _, part := range parts {
		if !isBareSegment(part) && !isBracketedPart(part) {
			return false
		}
	}
	return true
}

// isColumnName reports whether s is a one-part identifier: what an INSERT column list, a
// derived table's column list, an alias and a CTE name can hold.
func isColumnName(s string) bool {
	parts, ok := splitParts(s)
	return ok && len(parts) == 1 && isIdentifier(s)
}

func isBareSegment(part string) bool {
	return bareSegment.MatchString(part) && !floatLiteral.MatchString(part) && !binaryLiteral.MatchString(part)
}

// isBracketedPart reports whether part is a well-formed bracketed name the dialect may emit:
// one with at least one character between the brackets, and neither "@" nor "?" (see
// isIdentifier for why those are refused even inside the brackets).
func isBracketedPart(part string) bool {
	return len(part) > 2 && isWellFormedBracket(part) && !strings.ContainsAny(part, "@?")
}

// isWellFormedBracket reports whether part is one bracket pair, "[" … "]", with every "]"
// inside it doubled. The empty [] counts, so that QuoteIdentifier, which writes it for an
// empty part, leaves it alone the second time; it is not a name the server accepts, and
// isBracketedPart refuses it.
func isWellFormedBracket(part string) bool {
	if len(part) < 2 || part[0] != '[' || part[len(part)-1] != ']' {
		return false
	}
	inner := part[1 : len(part)-1]
	for i := 0; i < len(inner); i++ {
		if inner[i] != ']' {
			continue
		}
		if i+1 < len(inner) && inner[i+1] == ']' {
			i++
			continue
		}
		return false
	}
	return true
}

// hasBracketedMarker reports whether s would be a name isIdentifier accepts but for a bracketed
// part holding "@" or "?". The positions that must hold a name refuse it through isIdentifier;
// a table reference, a select item and a GROUP BY item emit what is not an identifier as the
// caller's SQL, and ask this first, so that [Orders@2024] is refused there too instead of
// being sent for gorm to misread (see isIdentifier).
func hasBracketedMarker(s string) bool {
	parts, ok := splitParts(s)
	if !ok || len(parts) == 0 || len(parts) > maxIdentifierParts {
		return false
	}
	marked := false
	for _, part := range parts {
		switch {
		case isBareSegment(part):
		case len(part) > 2 && isWellFormedBracket(part):
			marked = marked || strings.ContainsAny(part, "@?")
		default:
			return false
		}
	}
	return marked
}

// markerRefusal refuses the name s, which hasBracketedMarker found holding "@" or "?".
func markerRefusal(s string) error {
	return unsupported(fmt.Sprintf("the bracketed name %s", s),
		"gorm reads a \"?\" anywhere in a statement as a placeholder, and an \"@\" as the start of a "+
			"named parameter, so the arguments after it would be misplaced; rename the object, or reach "+
			"it through a view or synonym whose name has neither")
}

// refuseMarkedNames refuses the first of names that hasBracketedMarker finds holding "@" or "?".
func refuseMarkedNames(names ...string) error {
	for _, name := range names {
		if hasBracketedMarker(name) {
			return markerRefusal(name)
		}
	}
	return nil
}

// splitParts splits s on the dots that are outside brackets, so [a.b].c is two parts. ok is
// false when a bracket is left open.
func splitParts(s string) (parts []string, ok bool) {
	start := 0
	inBracket := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inBracket && c == ']':
			if i+1 < len(s) && s[i+1] == ']' {
				i++
				continue
			}
			inBracket = false
		case !inBracket && c == '[':
			inBracket = true
		case !inBracket && c == '.':
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	if inBracket {
		return nil, false
	}
	return append(parts, s[start:]), true
}

// tokens splits s on the white space outside brackets, so "[Order Lines] AS l" is three
// tokens.
func tokens(s string) []string {
	var out []string
	var current strings.Builder
	inBracket := false
	flush := func() {
		if current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case inBracket && r == ']':
			current.WriteRune(r)
			if i+1 < len(runes) && runes[i+1] == ']' {
				current.WriteRune(']')
				i++
				continue
			}
			inBracket = false
		case !inBracket && r == '[':
			inBracket = true
			current.WriteRune(r)
		case !inBracket && unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return out
}

// QuoteIdentifier brackets each part of a dotted name, doubling any "]" inside it:
// dbo.2024Orders becomes [dbo].[2024Orders], and a"b]c becomes [a"b]]c].
//
// It is idempotent — a part that is already a well-formed bracketed name is kept as it is, so
// [dbo].[2024Orders] comes back unchanged and [a.b].c becomes [a.b].[c] — and it cannot be
// broken out of: whatever the input, each part's text ends up inside one bracket pair. The
// niladic keywords (see niladicKeywords) come back bare, since bracketing one names a column
// that does not exist.
//
// It quotes whatever it is given. The dialect only hands it strings isIdentifier accepted; a
// caller that hands it anything else gets a name that is safe to emit, but not necessarily one
// the server resolves, and one that holds "@" or "?" misplaces the arguments bound after it
// (see isIdentifier).
func (d *SQLServerDialect) QuoteIdentifier(identifier string) string {
	return quoteIdentifier(identifier)
}

func quoteIdentifier(identifier string) string {
	if niladicKeywords[strings.ToUpper(identifier)] {
		return identifier
	}
	parts, ok := splitParts(identifier)
	if !ok {
		return bracket(identifier)
	}
	for i, part := range parts {
		if !isWellFormedBracket(part) {
			parts[i] = bracket(part)
		}
	}
	return strings.Join(parts, ".")
}

func bracket(part string) string {
	return "[" + strings.ReplaceAll(part, "]", "]]") + "]"
}

// unbracket returns the name a part stands for: the text between the brackets with "]]" read
// as "]", or the part itself when it is bare. Two spellings of one column compare equal on it.
func unbracket(part string) string {
	if isWellFormedBracket(part) {
		return strings.ReplaceAll(part[1:len(part)-1], "]]", "]")
	}
	return part
}

// isStarRef reports whether s is "*" or <identifier>.*, the whole-row reference of a SELECT
// list: t.* or dbo.t.*.
func isStarRef(s string) bool {
	if s == "*" {
		return true
	}
	prefix, ok := strings.CutSuffix(s, ".*")
	if !ok {
		return false
	}
	parts, split := splitParts(prefix)
	return split && len(parts) < maxIdentifierParts && isIdentifier(prefix)
}

func quoteStarRef(s string) string {
	if s == "*" {
		return s
	}
	return quoteIdentifier(strings.TrimSuffix(s, ".*")) + ".*"
}

// tableRef renders a table reference in FROM, JOIN or APPLY: t, t a and t AS a, where t is an
// identifier and a a one-part name, are bracketed as [t] and [t] AS [a]. Anything else — a
// table-valued function, a table hint, a #temp table, a name with an empty part such as
// db..t — is the caller's SQL and is emitted as written, except a name whose brackets hold
// "@" or "?" (see hasBracketedMarker), which is refused. aliased reports whether the
// reference carried an alias of its own.
func tableRef(ref string) (sql string, aliased bool, err error) {
	toks := tokens(ref)
	switch {
	case len(toks) == 1 && isIdentifier(toks[0]):
		return quoteIdentifier(toks[0]), false, nil
	case len(toks) == 2 && isIdentifier(toks[0]) && isColumnName(toks[1]):
		return quoteIdentifier(toks[0]) + " AS " + quoteIdentifier(toks[1]), true, nil
	case len(toks) == 3 && isIdentifier(toks[0]) && strings.EqualFold(toks[1], "AS") && isColumnName(toks[2]):
		return quoteIdentifier(toks[0]) + " AS " + quoteIdentifier(toks[2]), true, nil
	}
	if len(toks) >= 1 && len(toks) <= 3 {
		if err := refuseMarkedNames(toks[0], toks[len(toks)-1]); err != nil {
			return "", false, err
		}
	}
	return ref, false, nil
}

// filterClause finds the SQL-standard aggregate filter, COUNT(*) FILTER (WHERE …), which T-SQL
// does not have.
var filterClause = regexp.MustCompile(`(?i)\bFILTER\s*\(\s*WHERE\b`)

// selectItem renders one entry of a SELECT list: "*" as it is, t.* as [t].*, an identifier
// bracketed, and "<identifier> AS <name>" as [x] AS [name]. Anything else is an expression the
// caller wrote — COUNT(*), a CASE, a function call, a literal — and is emitted verbatim, with
// two refusals: a name whose brackets hold "@" or "?" (see hasBracketedMarker), alone or
// aliased, and an aggregate FILTER (WHERE …), which core.AggregateFunction renders and T-SQL
// has no spelling for.
func selectItem(item string) (string, error) {
	toks := tokens(item)
	switch {
	case len(toks) == 1 && isStarRef(toks[0]):
		return quoteStarRef(toks[0]), nil
	case len(toks) == 1 && isIdentifier(toks[0]):
		return quoteIdentifier(toks[0]), nil
	case len(toks) == 3 && isIdentifier(toks[0]) && strings.EqualFold(toks[1], "AS") && isColumnName(toks[2]):
		return quoteIdentifier(toks[0]) + " AS " + quoteIdentifier(toks[2]), nil
	case len(toks) == 1:
		if err := refuseMarkedNames(toks[0]); err != nil {
			return "", err
		}
	case len(toks) == 3 && strings.EqualFold(toks[1], "AS"):
		if err := refuseMarkedNames(toks[0], toks[2]); err != nil {
			return "", err
		}
	}
	if filterClause.MatchString(item) {
		return "", unsupported("an aggregate FILTER (WHERE …) clause",
			"T-SQL has no FILTER; move the condition into the aggregate, as in "+
				"SUM(CASE WHEN <condition> THEN x END) or COUNT(CASE WHEN <condition> THEN 1 END)")
	}
	return item, nil
}

// leadingDistinct matches a DISTINCT written into the first select item, as in
// Select("DISTINCT Email") or Select("DISTINCT(Email)"). The builder has no method that sets
// SelectClause.Distinct, so that is how an app asks for one, and the dialect has to see it
// there: T-SQL wants TOP after DISTINCT, not before it (Msg 156), and refuses the synthetic
// ORDER BY (SELECT NULL) under DISTINCT (Msg 145). A word boundary ends the keyword, so a
// column such as DISTINCTIVE is not one.
var leadingDistinct = regexp.MustCompile(`(?is)^\s*DISTINCT\b`)

// distinctOnRest matches what follows DISTINCT in Postgres's DISTINCT ON (…), written into an
// item the same way.
var distinctOnRest = regexp.MustCompile(`(?is)^ON\s*\(`)

// startsWithDistinct reports whether the first of fields opens with a DISTINCT; see
// leadingDistinct.
func startsWithDistinct(fields []string) bool {
	return len(fields) > 0 && leadingDistinct.MatchString(fields[0])
}

// liftDistinct takes a DISTINCT written into the first select item out of it, so the SELECT
// renders it in its own place, and reports whether there was one. What is left of the item
// renders as any item does, so "DISTINCT Email" becomes DISTINCT [Email]; an item that was only
// the keyword goes. A DISTINCT ON written that way is refused, as the builder's DistinctOn is.
func liftDistinct(fields []string) ([]string, bool, error) {
	if !startsWithDistinct(fields) {
		return fields, false, nil
	}
	loc := leadingDistinct.FindStringIndex(fields[0])
	rest := strings.TrimSpace(fields[0][loc[1]:])
	if distinctOnRest.MatchString(rest) {
		return nil, false, distinctOnRefusal()
	}

	lifted := make([]string, 0, len(fields))
	if rest != "" {
		lifted = append(lifted, rest)
	}
	return append(lifted, fields[1:]...), true, nil
}

// groupPosition matches a GROUP BY item that is a select-list position, as in GROUP BY 1.
var groupPosition = regexp.MustCompile(`^\s*[0-9]+\s*$`)

// groupItem renders one GROUP BY entry: an identifier bracketed, anything else — YEAR(x), a
// CASE — verbatim. Two are refused. A position, GROUP BY 1, which MySQL and Postgres read as
// the first select item: T-SQL has no GROUP BY position and reads it as the constant 1, which
// it refuses (Msg 164). And a name whose brackets hold "@" or "?" (see hasBracketedMarker).
func groupItem(item string) (string, error) {
	toks := tokens(item)
	if len(toks) == 1 && isIdentifier(toks[0]) {
		return quoteIdentifier(toks[0]), nil
	}
	if groupPosition.MatchString(item) {
		return "", unsupported("a GROUP BY position",
			fmt.Sprintf("T-SQL reads GROUP BY %s as a constant, since it has no GROUP BY position, nor a "+
				"GROUP BY of a select-list alias; group by the column or expression itself", strings.TrimSpace(item)))
	}
	if len(toks) == 1 {
		if err := refuseMarkedNames(toks[0]); err != nil {
			return "", err
		}
	}
	return item, nil
}

// columnName renders a name that must be a single column — an INSERT column, a conflict
// column, a MERGE SET target — or refuses it. role names the position in the refusal.
func columnName(name, role string) (string, error) {
	if !isColumnName(name) {
		return "", fmt.Errorf("sqlserver: the %s %q is not a column name; it must be a single "+
			"identifier such as Id, 2024Total or [Order Date], with no table prefix and no expression", role, name)
	}
	return quoteIdentifier(name), nil
}

// aliasName renders an alias or a CTE name, which must be a one-part identifier, or refuses it.
func aliasName(name, role string) (string, error) {
	if !isColumnName(name) {
		return "", fmt.Errorf("sqlserver: the %s %q is not a valid name; it must be a single "+
			"identifier such as o, recent_orders or [Order]", role, name)
	}
	return quoteIdentifier(name), nil
}
