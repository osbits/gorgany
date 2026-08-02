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
// The two dialects each had their own copy of this expression and applied it only to ORDER BY.
// It lives here so the condition family — which is where an unvalidated identifier actually
// became injectable — can use the same rule, and so the two engines cannot drift apart on what
// counts as a column name.
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
func identifierOperandSQL(operand any, args []any) (string, []any) {
	switch v := operand.(type) {
	case Raw:
		return string(v), args
	case Identifier:
		if IsSimpleIdentifier(string(v)) {
			return string(v), args
		}
		return "?", append(args, string(v))
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		return "(" + sql + ")", append(args, subArgs...)
	case string:
		if IsSimpleIdentifier(v) {
			return v, args
		}
		return "?", append(args, v)
	default:
		return "?", append(args, operand)
	}
}

// isIdentifierOperand reports whether this operand is safe in an identifier position without
// being demoted to a bound value. ValidatingCondition uses it to refuse rather than degrade.
func isIdentifierOperand(operand any) bool {
	switch v := operand.(type) {
	case Raw:
		return true
	case Identifier:
		return IsSimpleIdentifier(string(v))
	case *Query:
		return true
	case string:
		return IsSimpleIdentifier(v)
	default:
		// A non-string operand was already being bound before this change, so it is not an
		// identifier and never claimed to be.
		return true
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

func (c *BinaryCondition) Validate() error {
	return validateIdentifierSlot("the left side of a comparison", c.Left)
}

func (c *UnaryCondition) Validate() error {
	return validateIdentifierSlot("the operand of "+c.Operator, c.Operand)
}

func (c *InCondition) Validate() error {
	return validateIdentifierSlot("the field of an IN", c.Field)
}

func (c *BetweenCondition) Validate() error {
	return validateIdentifierSlot("the field of a BETWEEN", c.Field)
}

func (c *LikeCondition) Validate() error {
	return validateIdentifierSlot("the field of a LIKE", c.Field)
}

func (c *IsNullCondition) Validate() error {
	return validateIdentifierSlot("the field of an IS NULL", c.Field)
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

func validateIdentifierSlot(slot string, operand any) error {
	if isIdentifierOperand(operand) {
		return nil
	}
	return fmt.Errorf(
		"%s is %#v, which is not a column reference. A column goes in as-is; an expression "+
			"such as COUNT(*) or lower(email) must be wrapped in core.Raw so it is clear the "+
			"caller vouches for it as SQL", slot, operand)
}
