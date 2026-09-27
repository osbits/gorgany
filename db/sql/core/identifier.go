package core

import (
	"fmt"
	"regexp"
)

// Raw marks a string in an identifier position as SQL the caller vouches for. It is emitted
// verbatim.
//
// Never construct one from request-derived input. It exists so that a caller who genuinely
// needs an expression — `count(*)`, `lower(email)` — can say so explicitly, instead of the
// condition types guessing from the shape of a string, which is what they used to do.
type Raw string

// Identifier marks a string as a table or column reference.
//
// It is emitted verbatim when it really is an identifier and bound as a parameter when it is
// not, which is the same policy a bare string gets. What it adds is the ability to put an
// identifier in a *value* position — BinaryCondition.Right, where a bare string is correctly
// treated as a value — so a join can compare two columns rather than a column against the
// literal text of the other column's name.
type Identifier string

// simpleIdentifier matches a bare column or a dotted table.column reference, e.g.
// "created_at" or "members.created_at".
//
// The Postgres and MySQL dialects each had their own copy of this expression and applied it
// only to ORDER BY. It lives here so the condition family — which is where an unvalidated
// identifier actually became injectable — can use the same rule, and so those two engines
// cannot drift apart on what counts as a column name. SQL Server's dialect brackets every
// identifier and has a wider rule of its own (db/sql/gorm/sqlserver/v2/identifier.go).
var simpleIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)*$`)

// IsSimpleIdentifier reports whether s is a bare or dotted column reference and nothing else.
//
// Callers outside this package use it to validate configuration before it reaches a query —
// see model.DBFilter.Validate, where a "field" that is not an identifier is refused rather
// than emitted as SQL.
func IsSimpleIdentifier(s string) bool {
	return simpleIdentifier.MatchString(s)
}

// identifierOperandSQL renders an operand that sits in an identifier position.
//
// This is the hardening the condition family was missing. Every one of these types took a
// string in its field slot and emitted it verbatim:
//
//	BinaryCondition{Left: "author_name = 'x' OR 'y'='y'", Operator: "=", Right: true}
//	    →  author_name = 'x' OR 'y'='y' = ?     args=[true]
//
// which is a complete predicate of the caller's choosing, with a balanced placeholder count so
// nothing downstream notices. The ORDER BY path already refused to do this — see the dialects'
// orderByField, which quotes an identifier and binds anything else — and the WHERE path did
// not, which is the whole asymmetry SEC-H08 exploited.
//
//	Raw("count(*)")            → count(*)          verbatim, the caller vouches
//	Identifier("t.col")        → t.col             verbatim, validated
//	*Query                     → (SELECT …)        a subquery, as before
//	"created_at" / "t.col"     → created_at        a real identifier, verbatim
//	"a = 1 OR 1=1"             → ?    args=[…]     not an identifier: bound as a value
//	anything else              → ?    args=[…]     as before
//
// Binding a non-identifier rather than refusing keeps this a rendering decision: the condition
// types have no way to report an error, so the safe rendering is a bound value. The dialects
// additionally refuse outright — see ValidatingCondition — so a caller on the normal path gets
// told rather than silently getting a comparison against a string.
//
// That table is what a nil ctx renders, and ToSQL discards the error, so nothing on that path
// may produce one. Under a RenderContext the same rows go through the engine instead. What
// counts as an identifier is ctx.IsIdentifier, an identifier is emitted through
// ctx.QuoteIdentifier, and a subquery through ctx.Subquery. Under ctx.Strict anything that
// would be bound is refused by validateIdentifierSlot, the very check Validate runs, given the
// context's predicate, so the two refusals name slot and value in the same words and cannot
// come to disagree about what passes. slot is the name the refusal uses; it is not read on the
// nil path.
//
// The two paths share one switch and differ only in the rule, the quoter and where a subquery
// goes, so a change to what an operand renders as cannot reach one path and miss the other.
func identifierOperandSQL(operand any, args []any, ctx *RenderContext, slot string) (string, []any, error) {
	isIdentifier, quote := IsSimpleIdentifier, verbatimIdentifier
	if ctx != nil {
		if ctx.Strict {
			if err := validateIdentifierSlot(slot, operand, ctx.isIdentifier, true); err != nil {
				return "", nil, err
			}
		}
		isIdentifier, quote = ctx.isIdentifier, ctx.quoteIdentifier
	}

	switch v := operand.(type) {
	case Raw:
		return string(v), args, nil
	case Identifier:
		if isIdentifier(string(v)) {
			return quote(string(v)), args, nil
		}
		return "?", append(args, string(v)), nil
	case *Query:
		if ctx == nil {
			sql, subArgs := buildSubquerySQL(v)
			return "(" + sql + ")", append(args, subArgs...), nil
		}
		return subqueryOperandSQL(v, args, ctx)
	case string:
		if isIdentifier(v) {
			return quote(v), args, nil
		}
		return "?", append(args, v), nil
	default:
		return "?", append(args, operand), nil
	}
}

// verbatimIdentifier is the quoter of the nil path, which emits an identifier as written.
func verbatimIdentifier(identifier string) string { return identifier }

// isIdentifierOperand reports whether this operand is safe in an identifier position without
// being demoted to a bound value. ValidatingCondition uses it to refuse rather than degrade, with
// IsSimpleIdentifier as the rule; a Strict RenderContext uses it with its own.
//
// strict decides the one case the two disagree on: an operand that is not a string, an
// Identifier, a Raw or a *Query — nil, a number, a named string type such as an app's own
// column-name type. It is bound as a value either way. Validate lets it through, because it
// was being bound before Validate existed and refusing it now would fail queries that build
// today. A Strict context refuses it, because a value where a column belongs compares a
// constant — nil IS NULL, 'DeletedAt' IS NOT NULL — and so matches every row or none, which an
// UPDATE or DELETE filter must not do quietly.
func isIdentifierOperand(operand any, isIdentifier func(string) bool, strict bool) bool {
	switch v := operand.(type) {
	case Raw:
		return true
	case Identifier:
		return isIdentifier(string(v))
	case *Query:
		return true
	case string:
		return isIdentifier(v)
	default:
		// A non-string operand was already being bound before Validate existed, so it is not
		// an identifier and never claimed to be; whether that is refused is strict's call.
		return !strict
	}
}

// ValidatingCondition is implemented by conditions that can refuse to render.
//
// A dialect calls Validate before ToSQL and turns a refusal into a query-build error, so a
// condition that would otherwise have degraded quietly — a "column" that is really a predicate,
// bound as a string and compared against — fails loudly instead. The degrading behaviour is
// still what ToSQL does on its own, because ToSQL cannot report anything; this is how the
// normal path gets told.
type ValidatingCondition interface {
	Validate() error
}

// ValidateCondition validates a condition when it can be, and says nothing when it cannot.
func ValidateCondition(condition Condition) error {
	if validating, ok := condition.(ValidatingCondition); ok {
		return validating.Validate()
	}
	return nil
}

// --- Validate on the condition types whose identifier slot can be misused ---
//
// Each names the slot and the offending value, because the fix is always the same and the
// caller has to be told which of the two it is: a column reference belongs in the slot as it
// is, and an expression — COUNT(*), lower(email) — belongs in a Raw.
//
// The slot names are shared with the Strict render path (identifierOperandSQL), so a refusal
// reads the same whichever of the two raised it.

const (
	slotComparisonLeft  = "the left side of a comparison"
	slotComparisonRight = "the right side of a comparison"
	slotInField         = "the field of an IN"
	slotBetweenField    = "the field of a BETWEEN"
	slotLikeField       = "the field of a LIKE"
	slotIsNullField     = "the field of an IS NULL"
	slotRawPlaceholder  = `a "?." placeholder in a RawCondition`
)

func unaryOperandSlot(operator string) string {
	return "the operand of " + operator
}

func (c *BinaryCondition) Validate() error {
	return validateIdentifierSlot(slotComparisonLeft, c.Left, IsSimpleIdentifier, false)
}

func (c *UnaryCondition) Validate() error {
	return validateIdentifierSlot(unaryOperandSlot(c.Operator), c.Operand, IsSimpleIdentifier, false)
}

func (c *InCondition) Validate() error {
	return validateIdentifierSlot(slotInField, c.Field, IsSimpleIdentifier, false)
}

func (c *BetweenCondition) Validate() error {
	return validateIdentifierSlot(slotBetweenField, c.Field, IsSimpleIdentifier, false)
}

func (c *LikeCondition) Validate() error {
	return validateIdentifierSlot(slotLikeField, c.Field, IsSimpleIdentifier, false)
}

func (c *IsNullCondition) Validate() error {
	return validateIdentifierSlot(slotIsNullField, c.Field, IsSimpleIdentifier, false)
}

// Validate walks the operands so a nested condition cannot smuggle one past the check.
func (c *CompositeCondition) Validate() error {
	if c == nil {
		return nil
	}
	for _, condition := range c.Conditions {
		if err := ValidateCondition(condition); err != nil {
			return err
		}
	}
	return nil
}

// validateIdentifierSlot refuses an operand that isIdentifier would demote to a bound value.
// Validate passes IsSimpleIdentifier; a Strict RenderContext passes its own predicate, so an
// engine that accepts more names than Postgres and MySQL do still refuses in these words.
// strict is isIdentifierOperand's: only a Strict context refuses an operand that is not a
// string of the framework's own types, and only it can raise the second message below.
func validateIdentifierSlot(slot string, operand any, isIdentifier func(string) bool, strict bool) error {
	if isIdentifierOperand(operand, isIdentifier, strict) {
		return nil
	}
	switch operand.(type) {
	case string, Identifier:
		return fmt.Errorf(
			"%s is %#v, which is not a column reference. A column goes in as-is; an expression "+
				"such as COUNT(*) or lower(email) must be wrapped in core.Raw so it is clear the "+
				"caller vouches for it as SQL", slot, operand)
	}
	// %#v alone would print an app's named string type as a plain quoted string, which reads
	// like a column the refusal has no reason to refuse; the type says why it is refused.
	value := "nil"
	if operand != nil {
		value = fmt.Sprintf("%T(%#v)", operand, operand)
	}
	return fmt.Errorf(
		"%s is %s, which is not a column reference and would be compared as a value. A column "+
			"goes in as a string or a core.Identifier; an expression must be wrapped in core.Raw "+
			"so it is clear the caller vouches for it as SQL", slot, value)
}
