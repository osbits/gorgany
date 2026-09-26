package core

// The capabilities below are optional interfaces a dialect may implement, each with a probe
// that answers for any dialect. None of them is a method on SQLDialect: adding one there
// would stop every dialect an app has written from compiling, for a question most engines
// answer the same way. A dialect that says nothing gets the answer Postgres and MySQL need,
// which is also what callers did before the question could be asked.

// BindParameterLimiter is implemented by dialects whose engine caps the number of bind
// parameters one statement may carry.
//
// SQL Server refuses a request with more than 2100 parameters, a number a multi-row INSERT or
// a long IN list reaches quickly, and the refusal only arrives from the server. The number a
// dialect reports is what is left for the statement's own placeholders, not the server's cap:
// go-mssqldb sends a parameterised statement through sp_executesql, whose @stmt and @params
// take two of the 2100, so a SQL Server dialect reports 2098. A caller that splits work into
// batches asks BindParameterLimit first and sizes the batches to fit.
//
// The shipped Postgres and MySQL dialects do not implement it. Both engines cap a statement at
// 65535 bind parameters, but neither dialect declares that, so callers do not chunk for them;
// an IN over more keys than that, such as a relation load over a very large parent set, fails
// at the server as it always has.
type BindParameterLimiter interface {
	// MaxBindParameters is the most bind parameters one statement may carry. Zero or less
	// means there is no limit to respect.
	MaxBindParameters() int
}

// BindParameterLimit reports how many bind parameters d allows in one statement, or 0 when d
// declares no limit or is nil.
//
// Like SupportsReturning, it asks the dialect rather than keeping a table of engines beside
// it: the dialect is what renders the statement, so it is the one place the answer cannot
// drift from.
func BindParameterLimit(d SQLDialect) int {
	limiter, ok := d.(BindParameterLimiter)
	if !ok {
		return 0
	}
	if limit := limiter.MaxBindParameters(); limit > 0 {
		return limit
	}
	return 0
}

// TriggerSensitiveReturning is implemented by dialects whose RETURNING clause the server
// refuses on a table that has triggers.
//
// SQL Server spells RETURNING as OUTPUT INSERTED.<col>, and refuses an OUTPUT clause without
// INTO when the target table has an enabled trigger (Msg 334). The refusal arrives at
// execution, and the tables that carry triggers are typically ones whose schema the
// application does not own. SupportsReturning is still true for such a dialect, since most
// tables have none; this says the answer depends on the table, so a caller that knows its
// table has triggers reads generated keys back another way.
type TriggerSensitiveReturning interface {
	// ReturningBlockedByTriggers reports whether a trigger on the target table makes the
	// server refuse the dialect's RETURNING clause.
	ReturningBlockedByTriggers() bool
}

// ReturningBlockedByTriggers reports whether d's RETURNING clause is refused on a table with
// triggers. It is false for a nil dialect and for one that does not say, which covers
// Postgres and MySQL: Postgres's RETURNING works whatever triggers the table has, and MySQL
// has no RETURNING at all (see SupportsReturning).
func ReturningBlockedByTriggers(d SQLDialect) bool {
	sensitive, ok := d.(TriggerSensitiveReturning)
	return ok && sensitive.ReturningBlockedByTriggers()
}
