package v2

import (
	"errors"

	mssql "github.com/microsoft/go-mssqldb"
)

// serverErrorHints say what to change for the SQL Server errors an app on this engine is most
// likely to meet and least likely to decode from the server's own text, keyed by error number.
//
// Most of them come from pointing gorgany at a schema it does not own, EF Core's in
// particular: triggers, IDENTITY keys, rowversion and computed columns, and read-only
// replicas. The server's message names the symptom — "cannot insert explicit value for
// identity column" — and the hint names the model or config change that removes it.
var serverErrorHints = map[int32]string{
	334: "the target table has an enabled trigger, and SQL Server refuses an OUTPUT clause without INTO " +
		"there, which is what Returning, and the ORM's Create reading generated columns back, send; " +
		"insert through the executor's ExecInsert instead, which reads the key with SCOPE_IDENTITY()",
	544: "a value was sent for an IDENTITY column; leave the key zero so the server assigns it",
	271: "the column is computed; tag its field gorm:\"->\" so it is read and never written",
	272: "the column is a rowversion (timestamp); tag its field gorm:\"->\" so it is read and never written",
	273: "the column is a rowversion (timestamp); tag its field gorm:\"->\" so it is read and never written",
	257: "SQL Server does not make that conversion implicitly; when the value is a NULL for a varbinary " +
		"column, bind it as a []byte (nil for NULL), since a NULL with no type is sent as nvarchar",
	8003: "the request has more parameters than SQL Server's 2100; chunk long IN lists and batch " +
		"multi-row inserts (a statement may carry 2098 of its own)",
	10738: "a VALUES list holds at most 1000 rows; split the INSERT",
	1205: "the server ended this statement as a deadlock victim and rolled back its transaction, so " +
		"nothing it did was kept and it is safe to run again; concurrent upserts deadlock this way " +
		"when their multi-row batches share keys, so deduplicate and sort each batch's keys, keep " +
		"batches small, or retry",
	1033: "SQL Server refuses ORDER BY in a subquery, derived table or CTE unless TOP or OFFSET goes with it; " +
		"drop the ORDER BY, or add a Limit",
	8127: "the ORDER BY names a column the aggregate query does not select or group by; drop the ORDER BY " +
		"from a COUNT, or group by the column",
	8155: "every column of a derived table or CTE needs a name; alias each expression with AS",
	8156: "a derived table or CTE names the same column twice; give each column a distinct alias",
	3906: "the database is read-only here — a readable secondary or geo-replica; writes need the primary's host",
	18456: "the login failed: check username and password, or for Entra ID the account and auth.method, " +
		"and set auth.tenant_id when the account is a guest in the server's tenant",
	4060: "the login cannot open the database named in db: check the name, and that the login has a user in it",
	4063: "the login cannot open the database named in db: check the name, and that the login has a user in it",
}

// serverError is a SQL Server error with a hint appended. It unwraps to the driver's error, so
// errors.As(err, &mssql.Error{}) still finds the number.
type serverError struct {
	err  error
	hint string
}

func (e *serverError) Error() string { return e.err.Error() + "; " + e.hint }
func (e *serverError) Unwrap() error { return e.err }

// wrapServerError adds a hint to err when it carries a SQL Server error number
// serverErrorHints knows, and returns every other error as it is: nil, a refusal from a
// guard or the dialect, a context error.
//
// It looks at the error the server reported last and then at the ones before it, since a
// single request can fail with several numbers and the one that explains it is not always the
// last. The hint is fixed text: it never repeats a bound value or anything from the DSN.
func wrapServerError(err error) error {
	if err == nil {
		return nil
	}
	var serverErr mssql.Error
	if !errors.As(err, &serverErr) {
		return err
	}
	if hint, ok := serverErrorHints[serverErr.Number]; ok {
		return &serverError{err: err, hint: hint}
	}
	for _, each := range serverErr.All {
		if hint, ok := serverErrorHints[each.Number]; ok {
			return &serverError{err: err, hint: hint}
		}
	}
	return err
}
