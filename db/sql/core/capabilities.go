package core

import "context"

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

// TableTraits is what a database's catalog says about one table that decides how it may be
// written: what the server fills in itself, and what makes one of the ORM's statements fail
// there.
//
// It exists for tables gorgany does not own. A schema another system created — EF Core's, say
// — carries triggers, IDENTITY keys, rowversion and computed columns and server defaults that a
// hand-written model has to agree with, and every disagreement shows up only when a write
// reaches the server: an OUTPUT clause refused because of a trigger (Msg 334), an explicit
// value for an IDENTITY column (Msg 544), a write to a rowversion (Msg 272), or, silently, a
// default the INSERT overrode with a zero value. orm.VerifyModel reads the traits once, before
// the first write, and names each disagreement instead.
//
// The traits are read by the table's catalog identity, the object the server resolves the name
// to for this login, so they describe the table the ORM's statements write, and never a table
// of the same name in another schema. That is why they carry the columns themselves too.
//
// Every list holds column names as the catalog spells them, in the table's column order; the
// primary key's in key order.
type TableTraits struct {
	// Columns are every column of the table.
	Columns []string

	// NullableColumns are the columns that allow NULL.
	NullableColumns []string

	// InsertTriggers is how many enabled DML triggers fire on INSERT, AFTER or INSTEAD OF. SQL
	// Server refuses an INSERT's OUTPUT clause on such a table (Msg 334). A trigger that fires
	// only on UPDATE or DELETE does not block it, and a disabled trigger neither fires nor
	// blocks anything.
	InsertTriggers int

	// InsteadOfInsertTriggers is how many of InsertTriggers are INSTEAD OF triggers. The row
	// is then written by the trigger's own INSERT, in the trigger's scope, so SCOPE_IDENTITY()
	// after the statement is NULL and the key it generated cannot be read back by it.
	InsteadOfInsertTriggers int

	// UpdateTriggers is how many enabled DML triggers fire on UPDATE, which makes SQL Server
	// refuse an UPDATE's OUTPUT clause as InsertTriggers make it refuse an INSERT's.
	UpdateTriggers int

	// IdentityColumn is the table's IDENTITY (or equivalent auto-increment) column, or "" when
	// it has none. A table has at most one.
	IdentityColumn string

	// GeneratedColumns are the columns the server computes and refuses to be written: a
	// rowversion, a computed column and a GENERATED ALWAYS (temporal period) column.
	GeneratedColumns []string

	// DefaultColumns are the columns with a server default. An INSERT that names one writes
	// its own value, zero included, and the default never applies.
	DefaultColumns []string

	// PrimaryKey is the primary key's columns in key order, or nil when the table has none.
	PrimaryKey []string
}

// TableTraitsReporter is implemented by datasources that can read a table's TableTraits from
// their catalog.
//
// Unlike the capabilities above it is a datasource's, not a dialect's: the answer comes from a
// query, so it needs a connection. It is optional for the same reason they are, and a caller
// type-asserts it: a datasource that does not implement it simply cannot be asked, and
// orm.VerifyModel then checks what gorm's migrator reports about the columns alone. The SQL
// Server datasource implements it; Postgres and MySQL do not.
//
// An implementation must only read, in one statement, so it runs on a read_only datasource
// and one whose schema is owned elsewhere. table is written the way a model's TableName()
// writes it, such as 2024Orders, dbo.2024Orders or [dbo].[2024Orders], and resolved as the
// server resolves that name in the ORM's own statements: an unqualified name in the login's
// default schema. A table that does not exist is an error.
type TableTraitsReporter interface {
	TableTraits(ctx context.Context, table string) (TableTraits, error)
}
