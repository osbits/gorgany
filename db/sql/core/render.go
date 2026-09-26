package core

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// RenderContext is how an engine takes part in rendering the condition family.
//
// Conditions render themselves, and Condition.ToSQL takes no dialect. That is the boundary
// docs/DIALECTS.md describes ("Known boundary: conditions are dialect-independent"): every
// identifier a condition emits is verbatim, and every subquery nested in one goes through
// buildSubquerySQL, which spells LIMIT and OFFSET the Postgres/MySQL way and emits Postgres's
// DISTINCT ON. Postgres and MySQL mostly get away with that. An engine whose identifiers need
// quoting where theirs do not — a digit-leading table such as 2024Orders, a reserved word such
// as Order — cannot, because nothing it controls ever sees the condition's identifiers.
//
// The obvious fix is a dialect parameter on Condition.ToSQL, and it is the wrong one: it
// changes an interface every app-defined condition implements, so every one of them stops
// compiling. This seam is additive instead. A condition that can render under a context
// implements ContextRenderer alongside Condition; an engine that needs the seam calls
// RenderCondition, RenderWhere and RenderHaving with its own context; and nothing else
// moves. Each built-in condition's ToSQL is its ToSQLContext(nil), and a nil context renders
// what ToSQL always rendered, byte for byte and nil-versus-empty args included — which is
// what Postgres and MySQL still call, and what testdata/conditions.golden pins. The one
// deliberate exception is the RawCondition fix described at RawCondition.ToSQL. It changes a
// second render of a "?." RawCondition, and the first render of one whose "?." placeholders
// run short of args, which now falls back with every arg rather than the ones the expansion
// had not yet consumed. The golden leaves both out on purpose, and
// TestRawConditionToSQLIsIdempotent and TestRawConditionMalformedFallsBackToOriginalArgs pin
// the fixed output instead. An app-defined condition that knows nothing of the seam still
// renders: RenderCondition falls back to its ToSQL.
//
// Every hook is optional, and a nil hook keeps today's behaviour for its own slot, so a
// context only states what its engine does differently. A non-nil context also switches on
// corrections a nil one must not make, since a nil context is held to today's output and
// ToSQL has no way to report an error:
//
//   - a LIKE ESCAPE must be exactly one character other than a single quote;
//   - a WHERE clause or CompositeCondition skips parts that render empty, joins with AND
//     when its Operator is "", and spells AND and OR in upper case;
//   - a nil condition or a nil subquery is an error rather than a panic (a nil
//     *CompositeCondition or *RawCondition still renders as nothing, as it always has);
//   - a *WhereClause nested as a condition is parenthesised, as a CompositeCondition is, so
//     its operator cannot bind across the enclosing clause's;
//   - the identifiers a RawCondition's "?." placeholders substitute go through
//     QuoteIdentifier, and so does the literal column after "?." when IsIdentifier accepts
//     it.
//
// A context is only ever read, so one value can serve concurrent renders as long as its
// hooks can.
type RenderContext struct {
	// Dialect names the engine in the refusals the context itself raises (see
	// UnsupportedError). A hook's own errors say whatever the hook says.
	Dialect string

	// QuoteIdentifier renders an identifier the context accepted, e.g. [dbo].[2024Orders].
	// It receives the name exactly as the caller wrote it, dots and all. Nil emits it
	// verbatim.
	QuoteIdentifier func(identifier string) string

	// IsIdentifier decides what counts as an identifier in an identifier slot. Nil is
	// IsSimpleIdentifier, the Postgres/MySQL rule, which refuses a digit-leading name.
	IsIdentifier func(s string) bool

	// Subquery renders a *Query operand — IN (…), EXISTS (…), a subquery on either side of a
	// comparison, a BETWEEN bound, a LIKE pattern — as SQL *without* the surrounding
	// parentheses, which the condition adds. Nil is buildSubquerySQL, which renders the
	// subquery's own conditions without this context: a context that quotes identifiers
	// should set this too, or the subquery's are left as they are. A Strict context refuses
	// a subquery when this is nil.
	Subquery func(q *Query) (string, []any, error)

	// Operator maps the operator of a BinaryCondition or UnaryCondition — ILIKE to LIKE, say —
	// or refuses it with an error. The keywords the other conditions spell themselves (IN,
	// BETWEEN, LIKE, IS NULL, EXISTS) are portable and never pass through it. Nil emits the
	// operator verbatim, except under Strict (see below). A hook has the last word: whatever it
	// returns is emitted, so under Strict it is the hook that has to refuse what it does not
	// recognise.
	Operator func(op string) (string, error)

	// EmptyIn renders an IN whose value list is empty, which is invalid SQL written as the
	// "f IN ()" it becomes today. It replaces the whole predicate — "1=0" for IN and "1=1"
	// for NOT IN on an engine with no boolean literals — so the field's own args, if any,
	// are dropped with it. Nil keeps "f IN ()".
	EmptyIn func(not bool) string

	// Raw rewrites a RawCondition's SQL before its "?." placeholders are expanded, for a
	// dialect that has to adjust text it otherwise emits verbatim. Nil leaves it alone. It
	// is not applied to a core.Raw operand, which is an expression the caller wrote for the
	// engine it is targeting.
	Raw func(sql string) string

	// Strict turns every silent degradation into an error:
	//
	//   - anything in an identifier slot that would be bound as a value is refused, with the
	//     text Validate uses: a string or Identifier IsIdentifier rejects, and also — which
	//     Validate lets through — nil, a number or an app's named string type, which compare
	//     a constant and match every row or none;
	//   - an app-defined condition that does not render itself under a context (see
	//     RenderCondition) is validated (see ValidateCondition) before its ToSQL runs;
	//   - a WHERE clause or CompositeCondition may only join with AND or OR;
	//   - with no Operator hook, a BinaryCondition may only compare with =, <>, !=, <, <=, >,
	//     >=, LIKE or NOT LIKE, and a UnaryCondition may only be NOT, EXISTS or NOT EXISTS;
	//   - with no Subquery hook, a subquery is refused rather than rendered by
	//     buildSubquerySQL, which would render its conditions unquoted and unchecked;
	//   - a RawCondition whose "?." placeholders run out of args, or substitute something
	//     that is not an identifier, or are followed by a literal column that is not one, is
	//     refused instead of emitted as written.
	Strict bool
}

// ContextRenderer is implemented by conditions that can render under a RenderContext.
//
// It is separate from Condition so adding it breaks nothing: every built-in condition
// implements both, and an app-defined condition may implement it when it has identifiers or
// subqueries of its own that an engine needs to see. ToSQLContext(nil) must render exactly
// what ToSQL does.
//
// A struct that embeds a built-in condition — struct{ *core.BinaryCondition; tenant int } —
// implements it without saying so: Go promotes the embedded condition's ToSQLContext, and
// that renders the embedded condition alone, whatever the struct's own ToSQL adds to it or
// its own Validate refuses. Nothing at run time tells a promoted method from one the struct
// declares, so RenderCondition does not call ToSQLContext on a struct that embeds a
// ContextRenderer at all. It renders it the way it renders a condition that predates the
// seam, through its ToSQL, validated under Strict, so a tenant filter added in an overriding
// ToSQL is never dropped. A condition type that wants the seam and the built-in's rendering
// both holds the built-in in a named field and delegates to it from its own ToSQLContext.
type ContextRenderer interface {
	ToSQLContext(ctx *RenderContext) (string, []any, error)
}

// contextRendererType is ContextRenderer's reflect.Type, for embedsContextRenderer.
var contextRendererType = reflect.TypeFor[ContextRenderer]()

// Every built-in condition renders under a context, and RenderCondition calls each by its
// concrete type. A missing method would fail to compile there rather than send the type down
// the ToSQL fallback, unquoted; this list says the same where it can be read. *WhereClause and
// *HavingClause are Conditions too, and RenderCondition routes them explicitly.
var (
	_ ContextRenderer = (*BinaryCondition)(nil)
	_ ContextRenderer = (*UnaryCondition)(nil)
	_ ContextRenderer = (*InCondition)(nil)
	_ ContextRenderer = (*BetweenCondition)(nil)
	_ ContextRenderer = (*ExistsCondition)(nil)
	_ ContextRenderer = (*LikeCondition)(nil)
	_ ContextRenderer = (*IsNullCondition)(nil)
	_ ContextRenderer = (*RawCondition)(nil)
	_ ContextRenderer = (*CompositeCondition)(nil)
)

// errNilCondition refuses a nil Condition, and a nil pointer to any condition type that has no
// empty form of its own. A WHERE, JOIN ON or UPDATE/DELETE filter that silently lost a
// condition would match more rows than its caller asked for, so one is never skipped as if it
// were empty.
var errNilCondition = errors.New("cannot render a nil condition")

// RenderCondition renders c under ctx.
//
// With a nil ctx the result is c.ToSQL()'s, whatever c is; only a nil c is refused, since
// ToSQL on one would panic. Under a context:
//
//   - a built-in condition renders itself through its ToSQLContext;
//   - a *WhereClause or *HavingClause, which satisfy Condition and so can be nested in
//     another clause, renders as RenderWhere and RenderHaving render it, with a WHERE clause
//     parenthesised like a CompositeCondition, because its operator may not be the
//     enclosing clause's;
//   - an app-defined ContextRenderer renders itself, unless it is a struct that embeds one
//     (see ContextRenderer);
//   - anything else is an app-defined condition that predates the seam: under a Strict
//     context it is validated first, then its ToSQL runs as it always has.
func RenderCondition(c Condition, ctx *RenderContext) (string, []any, error) {
	if c == nil {
		return "", nil, errNilCondition
	}
	if ctx == nil {
		sql, args := c.ToSQL()
		return sql, args, nil
	}

	switch condition := c.(type) {
	case *BinaryCondition:
		return condition.ToSQLContext(ctx)
	case *UnaryCondition:
		return condition.ToSQLContext(ctx)
	case *InCondition:
		return condition.ToSQLContext(ctx)
	case *BetweenCondition:
		return condition.ToSQLContext(ctx)
	case *ExistsCondition:
		return condition.ToSQLContext(ctx)
	case *LikeCondition:
		return condition.ToSQLContext(ctx)
	case *IsNullCondition:
		return condition.ToSQLContext(ctx)
	case *RawCondition:
		return condition.ToSQLContext(ctx)
	case *CompositeCondition:
		return condition.ToSQLContext(ctx)
	case *WhereClause:
		if condition == nil {
			return "", nil, errNilCondition
		}
		return (&CompositeCondition{Operator: condition.Operator, Conditions: condition.Conditions}).contextSQL(ctx)
	case *HavingClause:
		if condition == nil {
			return "", nil, errNilCondition
		}
		return RenderHaving(condition, ctx)
	}

	if renderer, ok := c.(ContextRenderer); ok && !embedsContextRenderer(c) {
		return renderer.ToSQLContext(ctx)
	}
	if ctx.Strict {
		if err := ValidateCondition(c); err != nil {
			return "", nil, err
		}
	}
	sql, args := c.ToSQL()
	return sql, args, nil
}

// embedsContextRenderer reports whether c is a struct, or a pointer to one, with an embedded
// field that renders under a context — a built-in condition, or an app's own ContextRenderer.
// Go promotes that field's ToSQLContext to c unless c declares one, and the two cannot be
// told apart, so c's ToSQLContext is not known to render what c's ToSQL does. The check looks
// at the embedded fields' types, not at c's methods, and a field embedded deeper is caught
// through the field that embeds it, whose method set the promotion reaches as well.
func embedsContextRenderer(c Condition) bool {
	t := reflect.TypeOf(c)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous && (field.Type.Implements(contextRendererType) ||
			reflect.PointerTo(field.Type).Implements(contextRendererType)) {
			return true
		}
	}
	return false
}

// RenderWhere renders a WHERE clause's conditions under ctx, without the WHERE keyword.
//
// With a nil ctx it is w.ToSQL(). Under a context it skips parts that render empty rather
// than leaving a dangling operator, joins with AND when Operator is "" rather than with
// nothing, and under Strict refuses any operator but AND or OR. An empty result means there
// is no predicate at all. For an UPDATE or DELETE that is an unfiltered statement, so a
// dialect that refuses unfiltered writes has to check for "" itself.
func RenderWhere(w *WhereClause, ctx *RenderContext) (string, []any, error) {
	if w == nil {
		return "", nil, nil
	}
	if ctx == nil {
		sql, args := w.ToSQL()
		return sql, args, nil
	}

	operator, err := ctx.booleanOperator(w.Operator)
	if err != nil {
		return "", nil, err
	}
	parts, args, err := renderConditionParts(w.Conditions, ctx)
	if err != nil || len(parts) == 0 {
		return "", nil, err
	}
	return strings.Join(parts, " "+operator+" "), args, nil
}

// RenderHaving renders a HAVING clause's condition under ctx, without the HAVING keyword.
// With a nil ctx it is h.ToSQL(); under a context a condition that renders empty yields no
// predicate.
func RenderHaving(h *HavingClause, ctx *RenderContext) (string, []any, error) {
	if h == nil {
		return "", nil, nil
	}
	if ctx == nil {
		sql, args := h.ToSQL()
		return sql, args, nil
	}
	if h.Condition == nil {
		return "", nil, nil
	}

	sql, args, err := RenderCondition(h.Condition, ctx)
	if err != nil || strings.TrimSpace(sql) == "" {
		return "", nil, err
	}
	return sql, args, nil
}

// renderConditionParts renders each condition under ctx, dropping the ones that render
// empty along with any args they returned, so the parts and args stay aligned.
func renderConditionParts(conditions []Condition, ctx *RenderContext) ([]string, []any, error) {
	var parts []string
	var args []any
	for _, condition := range conditions {
		sql, conditionArgs, err := RenderCondition(condition, ctx)
		if err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(sql) == "" {
			continue
		}
		parts = append(parts, sql)
		args = append(args, conditionArgs...)
	}
	return parts, args, nil
}

// subqueryOperandSQL renders a *Query operand under ctx, parenthesised, appending its args.
func subqueryOperandSQL(query *Query, args []any, ctx *RenderContext) (string, []any, error) {
	sql, subArgs, err := ctx.subquery(query)
	if err != nil {
		return "", nil, err
	}
	return "(" + sql + ")", append(args, subArgs...), nil
}

// valueOperandSQL renders an operand in a value position under ctx — a BETWEEN bound, a LIKE
// pattern: a subquery goes through the context, and anything else is bound.
func valueOperandSQL(operand any, args []any, ctx *RenderContext) (string, []any, error) {
	if query, ok := operand.(*Query); ok {
		return subqueryOperandSQL(query, args, ctx)
	}
	return "?", append(args, operand), nil
}

func (ctx *RenderContext) dialectName() string {
	if ctx.Dialect == "" {
		return "this dialect"
	}
	return ctx.Dialect
}

func (ctx *RenderContext) isIdentifier(s string) bool {
	if ctx.IsIdentifier != nil {
		return ctx.IsIdentifier(s)
	}
	return IsSimpleIdentifier(s)
}

func (ctx *RenderContext) quoteIdentifier(identifier string) string {
	if ctx.QuoteIdentifier != nil {
		return ctx.QuoteIdentifier(identifier)
	}
	return identifier
}

// subquery renders q without parentheses. A nil q is refused here, where there is an error
// to return; buildSubquerySQL, which ToSQL still reaches, dereferences it. Without a hook a
// Strict context refuses q too: buildSubquerySQL renders q's WHERE, HAVING and JOIN ON through
// ToSQL, so nothing in them would be quoted, identifier-checked or operator-checked, and an
// input the context refuses at the top level would pass one level down.
func (ctx *RenderContext) subquery(q *Query) (string, []any, error) {
	if q == nil {
		return "", nil, errors.New("cannot render a nil subquery")
	}
	if ctx.Subquery != nil {
		return ctx.Subquery(q)
	}
	if ctx.Strict {
		return "", nil, fmt.Errorf("a Strict RenderContext for %s cannot render a subquery without a "+
			"Subquery hook: set RenderContext.Subquery, so the subquery's conditions render under "+
			"the context as well", ctx.dialectName())
	}
	sql, args := buildSubquerySQL(q)
	return sql, args, nil
}

// portableComparisons and portableUnaryOperators are what a Strict context with no Operator
// hook lets a BinaryCondition and a UnaryCondition use: operators every engine spells the same
// way, matched without regard to case or spacing, so the framework's own lower-case "like"
// and "not like" get through as written.
var (
	portableComparisons = map[string]bool{
		"=": true, "<>": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true,
		"LIKE": true, "NOT LIKE": true,
	}
	portableUnaryOperators = map[string]bool{"NOT": true, "EXISTS": true, "NOT EXISTS": true}
)

// operator resolves the operator of a BinaryCondition or UnaryCondition, given the operators
// portable to every engine for that kind of condition. The hook has the last word when there
// is one. Without one the operator is emitted as written, as ToSQL emits it, except under
// Strict: the operator is SQL placed next to the operands, so one taken from input —
// "= 1 OR 1=1 --" — is refused rather than emitted, as booleanOperator refuses one placed
// between two predicates.
func (ctx *RenderContext) operator(op string, portable map[string]bool) (string, error) {
	if ctx.Operator != nil {
		return ctx.Operator(op)
	}
	if ctx.Strict && !portable[strings.ToUpper(strings.Join(strings.Fields(op), " "))] {
		return "", Unsupported(ctx.dialectName(), fmt.Sprintf("the operator %q", op),
			"a Strict context with no Operator hook only emits operators every engine spells the same way")
	}
	return op, nil
}

func (ctx *RenderContext) rawSQL(sql string) string {
	if ctx.Raw != nil {
		return ctx.Raw(sql)
	}
	return sql
}

// booleanOperator resolves the operator a WHERE clause or CompositeCondition joins its parts
// with. "" is AND: the zero value would otherwise join them with nothing, as ToSQL does.
// Under Strict only AND and OR get through; anything else — MySQL's XOR, a typo, an
// operator taken from input — is refused rather than placed between two predicates.
func (ctx *RenderContext) booleanOperator(operator string) (string, error) {
	switch normalized := strings.ToUpper(strings.TrimSpace(operator)); normalized {
	case "":
		return "AND", nil
	case "AND", "OR":
		return normalized, nil
	}
	if ctx.Strict {
		return "", Unsupported(ctx.dialectName(), fmt.Sprintf("joining conditions with %q", operator), "use AND or OR")
	}
	return operator, nil
}

// placeholderIdentifier renders one identifier a RawCondition "?." placeholder substitutes.
// A core.Raw arg is the caller vouching for the text, as it is everywhere else, and is
// emitted verbatim. Anything else is the name fmt.Sprint gives it — what ToSQL has always
// substituted — quoted, and under Strict refused unless the context calls it an identifier.
func (ctx *RenderContext) placeholderIdentifier(arg any) (string, error) {
	if raw, ok := arg.(Raw); ok {
		return string(raw), nil
	}
	name := fmt.Sprint(arg)
	if ctx.Strict {
		if err := validateIdentifierSlot(slotRawPlaceholder, name, ctx.isIdentifier, true); err != nil {
			return "", err
		}
	}
	return ctx.quoteIdentifier(name), nil
}

// placeholderColumn renders the literal column a "?.<column>" placeholder writes after the
// identifier it substitutes. The caller wrote it, but it sits in an identifier position just as
// the table before it does, and T-SQL needs a reserved or digit-leading name delimited even
// after a dot: "?.Order" has to become [t].[Order], not [t].Order. So a column the context
// accepts is quoted, one it does not is refused under Strict and left as written otherwise,
// and one the caller delimited already — the scanner reads a double quote as part of the
// column — is left alone, as is the empty column of "?.*".
func (ctx *RenderContext) placeholderColumn(column string) (string, error) {
	if column == "" || strings.Contains(column, `"`) {
		return column, nil
	}
	if ctx.isIdentifier(column) {
		return ctx.quoteIdentifier(column), nil
	}
	if ctx.Strict {
		return "", fmt.Errorf("the column %q after a \"?.\" placeholder in a RawCondition is not a "+
			"column reference %s accepts. Pass it as the second arg of a \"?.?\" placeholder "+
			"instead, wrapped in core.Raw if it is SQL the caller vouches for", column, ctx.dialectName())
	}
	return column, nil
}
