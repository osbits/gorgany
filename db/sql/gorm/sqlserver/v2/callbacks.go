package v2

import (
	"fmt"

	"github.com/osbits/gorgany/v2/db/sql/gorm/internal/refused"
	"gorm.io/gorm"
)

// paramLimitCallback is the name the bind-parameter cap is registered under on each of
// gorm's processors. gorm keys callbacks by name, which is what lets an install tell it
// already ran.
const paramLimitCallback = "gorgany:sqlserver_param_limit"

// tooManyParameters is the refusal of a statement with n bind parameters, from the dialect
// and from the callback alike, so both say the same thing.
func tooManyParameters(n int) error {
	return unsupported(fmt.Sprintf("a statement with %d bound parameters", n),
		fmt.Sprintf("SQL Server accepts at most %d per request; chunk IN lists or batch inserts", MaxBindParameters))
}

// installParamLimit makes db refuse a statement with more bind parameters than SQL Server
// takes, before it is sent.
//
// The dialect refuses such a statement when it renders it, but it counts the arguments it
// rendered, and gorm is what turns a slice bound to IN (?) into one parameter per element. A
// raw statement from ExecRaw, FindRaw or an app's db.Raw is not rendered by the dialect at
// all. gorm builds Statement.Vars inside Raw and Exec, before any callback runs, so a
// callback ahead of gorm's own sees the count the server would, slices expanded. Without it
// the server refuses the request with Msg 8003, after the round trip, and an IN over a
// parent set of a few thousand keys is exactly how an app finds that out in production.
//
// It is registered with Before("*") on Query, Row and Raw, the processors that run SQL set
// with db.Raw or db.Exec, whose Vars exist before any callback runs. SQL that gorm builds
// itself is not counted: its chain API (Where("id IN ?", ids).Find(…)) builds the statement
// inside the Query processor's own gorm:query callback, after this one has seen no Vars, and
// Create, Update and Delete build theirs inside the callback that sends it too. No callback
// can count those first, so such a statement reaches the server, which refuses it with Msg
// 8003. Everything the Executor sends is Raw or Exec, and is counted. A refused Row()
// leaves a *sql.Row that reports the refusal rather than nil, on which the caller's Scan would
// panic. Installing it again adds nothing.
func installParamLimit(db *gorm.DB) error {
	callbacks := db.Callback()
	for _, processor := range []struct {
		installed func(name string) func(*gorm.DB)
		register  func(name string, fn func(*gorm.DB)) error
		handler   func(*gorm.DB)
	}{
		{callbacks.Query().Get, callbacks.Query().Before("*").Register, checkParamLimit},
		{callbacks.Row().Get, callbacks.Row().Before("*").Register, checkRowParamLimit},
		{callbacks.Raw().Get, callbacks.Raw().Before("*").Register, checkParamLimit},
	} {
		if processor.installed(paramLimitCallback) != nil {
			continue
		}
		if err := processor.register(paramLimitCallback, processor.handler); err != nil {
			return fmt.Errorf("sqlserver: registering %s: %w", paramLimitCallback, err)
		}
	}
	return nil
}

// checkParamLimit refuses a statement that already carries more than MaxBindParameters bind
// parameters. A statement that has already failed is left alone, so the first error stays the
// one errors.Is finds.
func checkParamLimit(db *gorm.DB) {
	if db.Error != nil || db.Statement == nil {
		return
	}
	if n := len(db.Statement.Vars); n > MaxBindParameters {
		db.AddError(tooManyParameters(n))
	}
}

// checkRowParamLimit is checkParamLimit for the Row processor, which serves both Row() and
// Rows(). Rows() returns the statement's error; Row() returns the *sql.Row left in Dest, so a
// refusal there leaves one that reports it.
func checkRowParamLimit(db *gorm.DB) {
	failed := db.Error != nil
	checkParamLimit(db)
	if failed || db.Error == nil {
		return
	}
	if rows, _ := db.Get("rows"); rows != true {
		db.Statement.Dest = refused.Row(db.Error)
	}
}
