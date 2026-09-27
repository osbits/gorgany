package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/gorm/internal/rendered"
	"gorm.io/gorm"
)

// Executor implements core.IQueryExecutor and core.LastInsertIDExecutor over a *gorm.DB.
//
// Every statement goes through gorm's Raw or Exec, including ExecInsert, which on Postgres
// and MySQL sends straight to the pool. That is what turns the dialect's "?" placeholders into
// go-mssqldb's @p1…@pN, and what runs the callbacks the datasource installed — the parameter
// cap and the guards — on every statement. Statements a builder rendered are marked as the
// dialect's (see rendered.Mark), so a read-only guard gives them the shape check; the raw
// methods are not marked, and get the deep one.
//
// Every method that renders a builder checks ToSQL's error first, so a construct T-SQL cannot
// express surfaces as QueryResult.Error naming the construct instead of as a syntax error
// from the server. SQL Server's own errors come back with a hint when the number is one an
// app on this engine is likely to meet (see wrapServerError).
type Executor struct {
	db *gorm.DB
}

var (
	_ core.IQueryExecutor       = (*Executor)(nil)
	_ core.LastInsertIDExecutor = (*Executor)(nil)
)

// NewExecutor creates a query executor over db.
func NewExecutor(db *gorm.DB) *Executor {
	return &Executor{db: db}
}

// rendered returns the handle a statement the dialect rendered runs on.
func (e *Executor) rendered(ctx context.Context) *gorm.DB {
	return rendered.Mark(e.db.WithContext(ctx))
}

// Exec runs a query for its effect and reports how many rows it affected.
//
// For an INSERT, UPDATE, DELETE or upsert the count is the statement's own. go-mssqldb
// reports the rows every statement of a request touched, a trigger's included, so a trigger
// without SET NOCOUNT ON that writes an audit row makes a one-row UPDATE report two, and an
// UPDATE guarded by a version column that matched nothing report a success. So the write
// runs as "<statement>; SELECT ROWCOUNT_BIG() AS [affected]" and the count is that row. A
// write with Returning has an OUTPUT clause, which a trigger table refuses anyway (Msg 334);
// there the count is the rows OUTPUT returned. Anything else runs as it is, and reports the
// driver's count.
func (e *Executor) Exec(ctx context.Context, query core.IQueryBuilder) core.QueryResult {
	sql, args, err := query.ToSQL()
	if err != nil {
		return core.QueryResult{Error: err}
	}

	built := query.Build()
	switch {
	case !isWrite(built):
		res := e.rendered(ctx).Exec(sql, bindArgs(args)...)
		return core.QueryResult{Error: wrapServerError(res.Error), RowsAffected: res.RowsAffected}
	case len(built.Returning) > 0:
		rows, err := e.rendered(ctx).Raw(sql, bindArgs(args)...).Rows()
		if err != nil {
			return core.QueryResult{Error: wrapServerError(err)}
		}
		affected, err := drainRows(rows)
		return core.QueryResult{Error: wrapServerError(err), RowsAffected: affected}
	default:
		var affected int64
		err := e.runTail(ctx, appendTail(sql, rowCountTail), args, rowCountColumns, &affected)
		return core.QueryResult{Error: err, RowsAffected: affected}
	}
}

// isWrite reports whether q is an INSERT, UPDATE, DELETE or upsert. A builder that exposes no
// query is taken for a read, which runs it unchanged.
func isWrite(q *core.Query) bool {
	return q != nil && (q.Insert != nil || q.Update != nil || q.Delete != nil)
}

// Find executes a query and scans the result into dest. An INSERT, UPDATE or DELETE with
// Returning scans the rows its OUTPUT clause returns, which is how the ORM reads generated
// columns back.
func (e *Executor) Find(ctx context.Context, query core.IQueryBuilder, dest interface{}) core.QueryResult {
	if dest == nil {
		return core.QueryResult{Error: errors.New("destination cannot be nil")}
	}

	sql, args, err := query.ToSQL()
	if err != nil {
		return core.QueryResult{Error: err}
	}

	res := e.rendered(ctx).Raw(sql, bindArgs(args)...).Scan(dest)
	return core.QueryResult{
		Error:        wrapServerError(res.Error),
		RowsAffected: res.RowsAffected,
		Found:        res.RowsAffected > 0,
	}
}

// Count executes a query whose first column is a count, and returns it.
func (e *Executor) Count(ctx context.Context, query core.IQueryBuilder) (int64, error) {
	sql, args, err := query.ToSQL()
	if err != nil {
		return 0, err
	}

	var count int64
	err = e.rendered(ctx).Raw(sql, bindArgs(args)...).Count(&count).Error
	return count, wrapServerError(err)
}

// ExecRaw executes a raw SQL statement.
//
// Its row count is the driver's, which is every row the request touched: a trigger's writes
// are counted in, unlike Exec's. Use Exec, or append "; SELECT ROWCOUNT_BIG()" and read it,
// when the count has to be the statement's own.
func (e *Executor) ExecRaw(ctx context.Context, sql string, args ...interface{}) core.QueryResult {
	res := e.db.WithContext(ctx).Exec(sql, bindArgs(args)...)
	return core.QueryResult{Error: wrapServerError(res.Error), RowsAffected: res.RowsAffected}
}

// FindRaw executes a raw SQL query and scans the result into dest.
func (e *Executor) FindRaw(ctx context.Context, dest interface{}, sql string, args ...interface{}) core.QueryResult {
	if dest == nil {
		return core.QueryResult{Error: errors.New("destination cannot be nil")}
	}

	res := e.db.WithContext(ctx).Raw(sql, bindArgs(args)...).Scan(dest)
	return core.QueryResult{
		Error:        wrapServerError(res.Error),
		RowsAffected: res.RowsAffected,
		Found:        res.RowsAffected > 0,
	}
}

// CountRaw executes a raw SQL COUNT query.
func (e *Executor) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	var count int64
	err := e.db.WithContext(ctx).Raw(sql, bindArgs(args)...).Count(&count).Error
	return count, wrapServerError(err)
}

// ExecInsert runs an INSERT and reports its IDENTITY key, implementing
// core.LastInsertIDExecutor.
//
// go-mssqldb has no LastInsertId, and Returning, which is OUTPUT here, is refused on a table
// with an enabled trigger (Msg 334). So the INSERT runs as one batch with
// "SELECT CAST(SCOPE_IDENTITY() AS BIGINT) AS [id], ROWCOUNT_BIG() AS [affected]" after it,
// which works on any table: SCOPE_IDENTITY() is the key this INSERT generated, never one a
// trigger's INSERT did, and it is read on the INSERT's own connection, in its transaction when
// there is one. HasLastInsertID is false when the table has no IDENTITY column, or the INSERT
// inserted nothing.
//
// The key is one value, the last the INSERT generated, so a multi-row INSERT reports the key
// of its last row. A query with Returning is refused: its OUTPUT rows would have nowhere to
// go, and Find is the method that reads them.
func (e *Executor) ExecInsert(ctx context.Context, query core.IQueryBuilder) core.InsertResult {
	statement, args, err := query.ToSQL()
	if err != nil {
		return core.InsertResult{QueryResult: core.QueryResult{Error: err}}
	}
	if built := query.Build(); built != nil {
		if built.Insert == nil {
			return core.InsertResult{QueryResult: core.QueryResult{
				Error: errors.New("sqlserver: ExecInsert runs an INSERT; use Exec for other statements")}}
		}
		if len(built.Returning) > 0 {
			return core.InsertResult{QueryResult: core.QueryResult{
				Error: errors.New("sqlserver: ExecInsert reads the key with SCOPE_IDENTITY() and cannot " +
					"also return OUTPUT rows; use Find for an INSERT with Returning")}}
		}
	}

	var (
		id       sql.NullInt64
		affected int64
	)
	if err := e.runTail(ctx, appendTail(statement, identityTail), args, identityColumns, &id, &affected); err != nil {
		return core.InsertResult{QueryResult: core.QueryResult{Error: err}}
	}
	return core.InsertResult{
		QueryResult:     core.QueryResult{RowsAffected: affected},
		LastInsertID:    id.Int64,
		HasLastInsertID: id.Valid && id.Int64 != 0,
	}
}

// byteArg is a []byte bound as one parameter.
//
// gorm expands a slice bound right after "(" into one parameter per element, so that
// IN (?) takes a slice, and a []byte is a slice: the first value of VALUES (?, …) arrived
// as one parameter per byte. A driver.Valuer is bound whole, so a []byte travels as a
// byteArg, whose Value is the bytes themselves, a varbinary to go-mssqldb.
type byteArg []byte

// Value returns the bytes, so a nil byteArg is a varbinary NULL.
func (b byteArg) Value() (driver.Value, error) {
	return []byte(b), nil
}

var bytesType = reflect.TypeOf([]byte(nil))

// bindArgs returns args ready to bind, in a new slice.
//
// A []byte, or any type whose underlying type is one, that is neither a driver.Valuer nor a
// gorm.Valuer becomes a byteArg, so it is bound whole wherever it stands. A *[]byte becomes
// the bytes it points to, and a nil one a nil byteArg: go-mssqldb declares an untyped NULL as
// nvarchar, which a varbinary column refuses (Msg 257), while a nil []byte is a varbinary
// NULL.
//
// Two values are sent as what they mean rather than as their Go shape. A json.RawMessage, and
// any other []byte type that implements json.Marshaler, is JSON text, and is bound as a
// string, an nvarchar: sent as varbinary to an nvarchar column, EF Core's JSON column type, the
// server converts it without an error by reading the bytes as UTF-16, and stores mojibake. A
// time.Time, a non-nil *time.Time and a valid sql.NullTime are converted to UTC. go-mssqldb
// sends a time as a datetimeoffset carrying its own offset, and converting that to datetime2
// or datetime, EF Core's column types for a DateTime, keeps the wall clock and drops the
// offset, so 10:00 in Zurich was stored as 10:00 and read back as 10:00 UTC, two hours out,
// and an equality lookup with the same time matched nothing. In UTC the instant is the same
// for a datetimeoffset column, and a datetime2 holds UTC, which is how go-mssqldb reads it
// back; Postgres and MySQL store the instant in UTC too. mssql.DateTimeOffset is left as it
// is, for a column that has to keep the offset itself.
//
// Every other value, slices bound to IN (?) included, is left for gorm to expand as it does.
func bindArgs(args []any) []any {
	if len(args) == 0 {
		return args
	}
	bound := make([]any, len(args))
	for i, arg := range args {
		bound[i] = bindArg(arg)
	}
	return bound
}

func bindArg(arg any) any {
	switch v := arg.(type) {
	case time.Time:
		return v.UTC()
	case *time.Time:
		if v == nil {
			return arg
		}
		return v.UTC()
	case sql.NullTime:
		if v.Valid {
			v.Time = v.Time.UTC()
		}
		return v
	case nil, driver.Valuer, gorm.Valuer:
		return arg
	case json.RawMessage:
		return string(v)
	case []byte:
		return byteArg(v)
	case *[]byte:
		if v == nil {
			return byteArg(nil)
		}
		return byteArg(*v)
	}
	value := reflect.ValueOf(arg)
	if value.Kind() == reflect.Slice && value.Type().ConvertibleTo(bytesType) {
		bytes := value.Convert(bytesType).Interface().([]byte)
		if _, isJSON := arg.(json.Marshaler); isJSON {
			return string(bytes)
		}
		return byteArg(bytes)
	}
	return arg
}
