package v2

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// The tails the executor appends to a write, so that one round trip returns what the driver
// cannot: go-mssqldb has no LastInsertId, and the row count it reports is every row every
// statement of the request touched, triggers included.
//
// Neither has an "@" in it. gorm switches Raw and Exec to named parameters when the SQL
// contains one, which is why the count is ROWCOUNT_BIG() and not @@ROWCOUNT.
const (
	// rowCountTail reads how many rows the statement before it affected. ROWCOUNT_BIG()
	// counts that statement alone, not what its triggers did.
	rowCountTail = "SELECT ROWCOUNT_BIG() AS [affected]"

	// identityTail reads the IDENTITY value the INSERT before it generated, in this scope — so
	// not one a trigger's own INSERT generated — and its row count. Both functions are read
	// in one SELECT, so ROWCOUNT_BIG() still describes the INSERT.
	identityTail = "SELECT CAST(SCOPE_IDENTITY() AS BIGINT) AS [id], ROWCOUNT_BIG() AS [affected]"
)

var (
	rowCountColumns = []string{"affected"}
	identityColumns = []string{"id", "affected"}
)

// errTailMissing reports a batch whose tail never answered: the request ran, but its result
// does not say how many rows it affected, so no count the executor could report is true.
var errTailMissing = errors.New("sqlserver: the statement's row-count tail was not returned")

// appendTail makes stmt and tail one batch. A trailing ";" on stmt is dropped and the batch's
// own separator stands in for it, so a MERGE, which must end with one, still does.
func appendTail(stmt, tail string) string {
	return strings.TrimRight(strings.TrimSpace(stmt), "; \t\r\n") + "; " + tail
}

// runTail runs batch, whose last statement is a tail selecting wantCols, and scans the tail's
// row into dest.
//
// It runs through gorm's Rows, never Row. Row returns a nil *sql.Row when a callback ahead of
// gorm's — a guard, the parameter cap — has refused the statement, and Scan on that panics;
// Rows returns the refusal as its error.
//
// The tail is found by its column names, because it is not always the only result set: a
// trigger that SELECTs without SET NOCOUNT ON sends one of its own ahead of it. The tail runs
// last, so when a trigger's result set happens to have the same columns, the last match is
// the one kept. Every result set is read to the end, and then the request's error is checked,
// then Close's: go-mssqldb reports an error a statement raised inside the request — a
// duplicate key, say — as it reads past it, and a request cut short by one has no tail to
// find.
func (e *Executor) runTail(ctx context.Context, batch string, args []any, wantCols []string, dest ...any) error {
	rows, err := e.rendered(ctx).Raw(batch, bindArgs(args)...).Rows()
	if err != nil {
		return wrapServerError(err)
	}

	found, err := scanTail(rows, wantCols, dest)
	if err != nil {
		_ = rows.Close()
		return wrapServerError(err)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return wrapServerError(err)
	}
	if err := rows.Close(); err != nil {
		return wrapServerError(err)
	}
	if !found {
		return errTailMissing
	}
	return nil
}

// scanTail walks every result set of rows to its end and scans the first row of the last one
// whose columns are wantCols into dest.
func scanTail(rows *sql.Rows, wantCols []string, dest []any) (bool, error) {
	found := false
	for {
		cols, err := rows.Columns()
		if err != nil {
			return found, err
		}
		if sameColumns(cols, wantCols) && rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				return found, err
			}
			found = true
		}
		for rows.Next() {
		}
		if !rows.NextResultSet() {
			return found, nil
		}
	}
}

// drainRows counts the rows of the first result set of rows, reads the rest to their end, and
// checks the request's error and Close's, as runTail does.
func drainRows(rows *sql.Rows) (int64, error) {
	var count int64
	first := true
	for {
		for rows.Next() {
			if first {
				count++
			}
		}
		first = false
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return count, err
	}
	return count, rows.Close()
}

// sameColumns reports whether got names exactly want, in order, ignoring case.
func sameColumns(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !strings.EqualFold(got[i], want[i]) {
			return false
		}
	}
	return true
}
