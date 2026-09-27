// Package refused makes the *sql.Row a gorm callback leaves behind when it refuses a
// statement, for the callbacks in db/sql/gorm that refuse one before it is sent: the policy
// guards in db/sql/gorm/guard and the SQL Server engine's bind-parameter cap.
//
// It exists because gorm's Row() returns whatever *sql.Row its callbacks leave in the
// statement's Dest, and a refused statement leaves none, so Row() returns nil and the
// caller's Scan panics. A refusal has to come back the way a database error would, through
// the row's Scan and Err, and only database/sql can make a *sql.Row that carries an error.
package refused

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"
)

// Row returns a *sql.Row whose Scan and Err report err.
//
// database/sql makes a *sql.Row that carries an error when a query's context is done before
// the query gets a connection, so Row asks a pool that never connects, with a context that is
// already done and gives err as the reason. Nothing is sent anywhere.
func Row(err error) *sql.Row {
	return refusalPool().QueryRowContext(refusedContext{err}, "")
}

// refusalPool is the pool Row asks. It is opened once, on the first refusal that needs it, and
// lives as long as the process, as does the goroutine database/sql starts for it.
var refusalPool = sync.OnceValue(func() *sql.DB { return sql.OpenDB(neverConnects{}) })

type neverConnects struct{}

var errNeverConnects = errors.New("refused: this pool never connects")

func (neverConnects) Connect(context.Context) (driver.Conn, error) { return nil, errNeverConnects }
func (neverConnects) Driver() driver.Driver                        { return neverConnects{} }
func (neverConnects) Open(string) (driver.Conn, error)             { return nil, errNeverConnects }

// refusedContext is a context that is done from the start, with err as the reason.
type refusedContext struct{ err error }

var done = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

func (refusedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (refusedContext) Done() <-chan struct{}       { return done }
func (c refusedContext) Err() error                { return c.err }
func (refusedContext) Value(any) any               { return nil }
