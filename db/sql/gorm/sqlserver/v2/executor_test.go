package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The executor, run through gorm's SQL Server dialector on a fake database/sql driver, so what
// is pinned is what go-mssqldb would be handed: the SQL after gorm's rewrite of "?" to @pN,
// the batches with their tails, the arguments as bound, and what never reaches the driver at
// all because a callback refused it first.

var ctx = context.Background()

// answer makes a fakeServer answer every statement with resp.
func answer(resp fakeResponse) *fakeServer {
	return &fakeServer{respond: func(string) fakeResponse { return resp }}
}

// ------------------------------------------------------------------- rewriting and batches

// TestExecRewritesQuestionMarksToAtP: the dialect writes "?", and gorm, not the executor,
// turns each into go-mssqldb's @pN. A write runs as one batch with its row-count tail.
func TestExecRewritesQuestionMarksToAtP(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{tail(1)}})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Update("dbo.2024Orders").Set("Status", "paid").Eq("Id", 7))
	require.NoError(t, res.Error)

	call := server.only(t)
	assert.Equal(t, "query", call.kind, "a batch with a tail is read, not executed")
	assert.Equal(t, "UPDATE [dbo].[2024Orders] SET [Status] = @p1 WHERE [Id] = @p2; SELECT ROWCOUNT_BIG() AS [affected]", call.query)
	assert.Equal(t, []any{"paid", 7}, call.args)
	assert.NotContains(t, call.query, "?")
}

// TestExecTakesRowsAffectedFromRowcountBig: go-mssqldb sums the rows every statement of a
// request touched, a trigger's included, so a trigger that writes an audit row would make a
// one-row UPDATE report two, and a guarded UPDATE that matched nothing report one. The count
// is the tail's, and a trigger's own result set ahead of it is skipped.
func TestExecTakesRowsAffectedFromRowcountBig(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"AuditId"}, rows: [][]driver.Value{{int64(10)}, {int64(11)}, {int64(12)}}},
		tail(1),
	}})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Delete("dbo.2024Orders").Eq("Id", 7))
	require.NoError(t, res.Error)
	assert.Equal(t, int64(1), res.RowsAffected)
	assert.True(t, strings.HasSuffix(server.only(t).query, "; SELECT ROWCOUNT_BIG() AS [affected]"))
}

// TestExecReportsZeroWhenNothingMatched is the case the tail exists for: an UPDATE guarded by
// a version column that matched no row must say so.
func TestExecReportsZeroWhenNothingMatched(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{tail(0)}})

	res := fakeExecutor(t, server).Exec(ctx,
		NewBuilder().Update("Orders").Set("Status", "paid").Eq("Id", 7).Eq("RowVersion", []byte{0, 1}))
	require.NoError(t, res.Error)
	assert.Zero(t, res.RowsAffected)
}

// TestExecWithOutputCountsScannedRows: a write with Returning carries OUTPUT, which a table
// with triggers refuses anyway, so its count is the rows OUTPUT returned, and no tail is sent.
func TestExecWithOutputCountsScannedRows(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"Id"}, rows: [][]driver.Value{{int64(1)}, {int64(2)}, {int64(3)}}},
	}})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Delete("t").Eq("a", 1).Returning("Id"))
	require.NoError(t, res.Error)
	assert.Equal(t, int64(3), res.RowsAffected)
	assert.Equal(t, "DELETE FROM [t] OUTPUT DELETED.[Id] WHERE [a] = @p1", server.only(t).query)
}

// TestExecOfAReadRunsAsItIs: only a write gets a tail. A read's count is the driver's.
func TestExecOfAReadRunsAsItIs(t *testing.T) {
	server := answer(fakeResponse{affected: 4})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Select("Id").From("t"))
	require.NoError(t, res.Error)
	assert.Equal(t, int64(4), res.RowsAffected)

	call := server.only(t)
	assert.Equal(t, "exec", call.kind)
	assert.Equal(t, "SELECT [Id] FROM [t]", call.query)
}

// TestExecPassesRefusalsThrough: a construct the dialect refuses never reaches the driver.
func TestExecPassesRefusalsThrough(t *testing.T) {
	server := &fakeServer{}

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Select("Id").From("t").DistinctOn("Id"))
	requireUnsupported(t, res.Error, "DISTINCT ON")
	assert.Empty(t, server.log())
}

// ------------------------------------------------------------------- ExecInsert

// TestExecInsertRunsOneBatchThroughGorm: go-mssqldb has no LastInsertId, and the Postgres and
// MySQL ExecInsert sends straight to the pool, past gorm's rewrite of "?". Here the INSERT and
// its key come back from one batch that gorm rewrote.
func TestExecInsertRunsOneBatchThroughGorm(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"id", "affected"}, rows: [][]driver.Value{{int64(42), int64(1)}}},
	}})

	res := fakeExecutor(t, server).ExecInsert(ctx, NewBuilder().Insert("dbo.2024Orders").Columns("Name").Values("a"))
	require.NoError(t, res.Error)

	call := server.only(t)
	assert.Equal(t, "query", call.kind)
	assert.Equal(t, "INSERT INTO [dbo].[2024Orders] ([Name]) VALUES (@p1); "+
		"SELECT CAST(SCOPE_IDENTITY() AS BIGINT) AS [id], ROWCOUNT_BIG() AS [affected]", call.query)
	assert.Equal(t, []any{"a"}, call.args)
}

func TestExecInsertReportsScopeIdentity(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"id", "affected"}, rows: [][]driver.Value{{int64(42), int64(1)}}},
	}})

	res := fakeExecutor(t, server).ExecInsert(ctx, NewBuilder().Insert("t").Columns("Name").Values("a"))
	require.NoError(t, res.Error)
	assert.True(t, res.HasLastInsertID)
	assert.Equal(t, int64(42), res.LastInsertID)
	assert.Equal(t, int64(1), res.RowsAffected)
}

// TestExecInsertWithNullIdentityReportsNoKey: a table without an IDENTITY column, or an
// upsert that inserted nothing, leaves SCOPE_IDENTITY() NULL, which is no key rather than 0.
func TestExecInsertWithNullIdentityReportsNoKey(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"id", "affected"}, rows: [][]driver.Value{{nil, int64(1)}}},
	}})

	res := fakeExecutor(t, server).ExecInsert(ctx, NewBuilder().Insert("t").Columns("Name").Values("a"))
	require.NoError(t, res.Error)
	assert.False(t, res.HasLastInsertID)
	assert.Zero(t, res.LastInsertID)
	assert.Equal(t, int64(1), res.RowsAffected)
}

// TestExecInsertRefusesReturning: its OUTPUT rows would have nowhere to go, and an OUTPUT is
// what a trigger table refuses in the first place.
func TestExecInsertRefusesReturning(t *testing.T) {
	server := &fakeServer{}

	res := fakeExecutor(t, server).ExecInsert(ctx, NewBuilder().Insert("t").Columns("Name").Values("a").Returning("Id"))
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "use Find")
	assert.Empty(t, server.log())
}

func TestExecInsertRefusesAStatementThatIsNotAnInsert(t *testing.T) {
	server := &fakeServer{}

	res := fakeExecutor(t, server).ExecInsert(ctx, NewBuilder().Delete("t").Eq("Id", 1))
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "runs an INSERT")
	assert.Empty(t, server.log())
}

// TestExecInsertStripsTrailingSemicolon: a MERGE must end with ";", and the batch's own
// separator is that ";" — a second one would be an empty statement.
func TestExecInsertStripsTrailingSemicolon(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"id", "affected"}, rows: [][]driver.Value{{int64(3), int64(1)}}},
	}})

	upsert := NewBuilder().Insert("t").Columns("Id", "Name").Values(1, "a").OnConflict("Id").
		DoUpdate(map[string]any{"Name": "b"})
	res := fakeExecutor(t, server).ExecInsert(ctx, upsert)
	require.NoError(t, res.Error)

	query := server.only(t).query
	assert.Contains(t, query, "[src].[Name]); SELECT CAST(SCOPE_IDENTITY() AS BIGINT)")
	assert.Equal(t, 1, strings.Count(query, ";"), "%s", query)
}

func TestAppendTail(t *testing.T) {
	assert.Equal(t, "MERGE x; SELECT 1", appendTail("MERGE x;", "SELECT 1"))
	assert.Equal(t, "MERGE x; SELECT 1", appendTail("  MERGE x ;; \n", "SELECT 1"))
	assert.Equal(t, "INSERT x; SELECT 1", appendTail("INSERT x", "SELECT 1"))
	assert.NotContains(t, rowCountTail+identityTail, "@", "an @ would switch gorm to named parameters")
}

// TestExecInsertReturnsGuardErrorInsteadOfPanicking: gorm's Row() returns a nil *sql.Row when
// a callback ahead of it has refused the statement, and Scan on that panics. The batch runs
// through Rows(), which returns the refusal.
func TestExecInsertReturnsGuardErrorInsteadOfPanicking(t *testing.T) {
	server := &fakeServer{}
	executor := NewExecutor(fakeGorm(t, server, true, false))

	var res dbCore.InsertResult
	require.NotPanics(t, func() {
		res = executor.ExecInsert(ctx, NewBuilder().Insert("t").Columns("Name").Values("a"))
	})
	require.ErrorIs(t, res.Error, dbCore.ErrReadOnly)
	assert.Empty(t, server.log(), "a refused statement never reaches the driver")
}

// TestTailSkipsTriggerResultSets: a trigger that SELECTs without SET NOCOUNT ON sends a
// result set of its own ahead of the tail.
func TestTailSkipsTriggerResultSets(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"AuditId", "Action"}, rows: [][]driver.Value{{int64(9), "insert"}}},
		{cols: []string{}},
		{cols: []string{"id", "affected"}, rows: [][]driver.Value{{int64(42), int64(1)}}},
	}})

	res := fakeExecutor(t, server).ExecInsert(ctx, NewBuilder().Insert("t").Columns("Name").Values("a"))
	require.NoError(t, res.Error)
	assert.Equal(t, int64(42), res.LastInsertID)
	assert.Equal(t, int64(1), res.RowsAffected)
}

// TestTailTakesTheLastMatchingResultSet: the tail runs last, so a trigger's result set that
// happens to have the tail's columns is not mistaken for it.
func TestTailTakesTheLastMatchingResultSet(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{
		{cols: []string{"affected"}, rows: [][]driver.Value{{int64(99)}}},
		tail(1),
	}})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Update("t").Set("a", 1).Eq("Id", 1))
	require.NoError(t, res.Error)
	assert.Equal(t, int64(1), res.RowsAffected)
}

// TestTailSurfacesErrorsReportedAfterIt: go-mssqldb reports an error a statement raised inside
// the request as the rows are read past it, which can be after the tail's row.
func TestTailSurfacesErrorsReportedAfterIt(t *testing.T) {
	duplicate := mssql.Error{Number: 2627, Message: "Violation of PRIMARY KEY constraint"}
	server := answer(fakeResponse{sets: []fakeSet{tail(1)}, nextErr: duplicate})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Update("t").Set("a", 1).Eq("Id", 1))
	var serverErr mssql.Error
	require.ErrorAs(t, res.Error, &serverErr)
	assert.Equal(t, int32(2627), serverErr.Number)
}

func TestAMissingTailIsAnError(t *testing.T) {
	server := answer(fakeResponse{})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Update("t").Set("a", 1).Eq("Id", 1))
	require.ErrorIs(t, res.Error, errTailMissing)
}

// TestTheBatchErrorCarriesItsHint: an error from the batch itself comes back with the hint
// for its number.
func TestTheBatchErrorCarriesItsHint(t *testing.T) {
	server := answer(fakeResponse{err: mssql.Error{Number: 544, Message: "Cannot insert explicit value for identity column"}})

	res := fakeExecutor(t, server).ExecInsert(ctx, NewBuilder().Insert("t").Columns("Id").Values(5))
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "leave the key zero")
	var serverErr mssql.Error
	require.ErrorAs(t, res.Error, &serverErr)
	assert.Equal(t, int32(544), serverErr.Number)
}

// ------------------------------------------------------------------- arguments

// TestByteSliceArgsAreNotExpanded: gorm expands a slice bound right after "(" into one
// parameter per element, and a []byte is a slice, so the first value of a VALUES list went to
// the server as one parameter per byte.
func TestByteSliceArgsAreNotExpanded(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{tail(1)}})

	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Insert("t").Columns("Data", "Name").Values([]byte{1, 2, 3}, "x"))
	require.NoError(t, res.Error)

	call := server.only(t)
	assert.True(t, strings.HasPrefix(call.query, "INSERT INTO [t] ([Data], [Name]) VALUES (@p1, @p2);"), call.query)
	require.Len(t, call.args, 2)
	assert.Equal(t, byteArg{1, 2, 3}, call.args[0])
	assert.Equal(t, "x", call.args[1])
}

// TestNilBytePointerBindsTypedNull: go-mssqldb declares an untyped NULL as nvarchar, which a
// varbinary column refuses (Msg 257). A nil *[]byte is bound as a nil []byte instead, which
// it sends as a varbinary NULL.
func TestNilBytePointerBindsTypedNull(t *testing.T) {
	server := answer(fakeResponse{affected: 1})

	res := fakeExecutor(t, server).ExecRaw(ctx, "UPDATE [t] SET [Data] = ? WHERE [Id] = ?", (*[]byte)(nil), 1)
	require.NoError(t, res.Error)

	call := server.only(t)
	require.Len(t, call.args, 2)
	valuer, ok := call.args[0].(driver.Valuer)
	require.True(t, ok, "bound as %T", call.args[0])
	value, err := valuer.Value()
	require.NoError(t, err)
	assert.IsType(t, []byte(nil), value, "a typed NULL, not an untyped nil")
	assert.Nil(t, value)
}

// TestIntSliceInRawSQLStillExpands: only bytes are bound whole; IN (?) takes a slice as it
// does on every engine.
func TestIntSliceInRawSQLStillExpands(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{{cols: []string{"Id"}}}})

	var rows []map[string]any
	res := fakeExecutor(t, server).FindRaw(ctx, &rows, "SELECT [Id] FROM [t] WHERE [Id] IN (?)", []int{1, 2, 3})
	require.NoError(t, res.Error)

	call := server.only(t)
	assert.Equal(t, "SELECT [Id] FROM [t] WHERE [Id] IN (@p1,@p2,@p3)", call.query)
	assert.Equal(t, []any{1, 2, 3}, call.args)
}

// blob is a named []byte type that is not JSON.
type blob []byte

// document is a named []byte type that says it is JSON, as json.RawMessage does.
type document []byte

func (d document) MarshalJSON() ([]byte, error) { return d, nil }

func TestBindArgs(t *testing.T) {
	data := []byte{1}
	valuer := byteArg{7}
	var nilBytes []byte

	bound := bindArgs([]any{data, &data, blob{2}, valuer, nil, nilBytes, [2]byte{1, 2}, []int{1}, "s"})
	assert.Equal(t, byteArg{1}, bound[0])
	assert.Equal(t, byteArg{1}, bound[1], "a *[]byte binds the bytes it points to")
	assert.Equal(t, byteArg{2}, bound[2], "a named []byte type is bytes too")
	assert.Equal(t, valuer, bound[3], "a Valuer binds itself")
	assert.Nil(t, bound[4])
	assert.Equal(t, byteArg(nil), bound[5])
	assert.Equal(t, [2]byte{1, 2}, bound[6], "an array is not a slice gorm expands")
	assert.Equal(t, []int{1}, bound[7])
	assert.Equal(t, "s", bound[8])
	assert.Empty(t, bindArgs(nil))

	// A string go-mssqldb is told to send as varchar, which is how a varchar key is compared
	// without converting the column (see docs/DIALECTS.md), goes through as it is.
	assert.Equal(t, []any{mssql.VarChar("k"), mssql.VarCharMax("k")}, bindArgs([]any{mssql.VarChar("k"), mssql.VarCharMax("k")}))
}

// TestJSONBytesBindAsText: JSON is text. Sent as varbinary to an nvarchar column, EF Core's
// JSON column type, the server converted it without an error by reading the bytes as UTF-16,
// and stored mojibake. So a json.RawMessage, and any []byte type that marshals itself as JSON,
// is bound as a string, which go-mssqldb sends as nvarchar.
func TestJSONBytesBindAsText(t *testing.T) {
	bound := bindArgs([]any{json.RawMessage(`{"a":1}`), document(`[1]`), json.RawMessage(nil)})
	assert.Equal(t, []any{`{"a":1}`, `[1]`, ""}, bound)

	server := answer(fakeResponse{sets: []fakeSet{tail(1)}})
	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Insert("dbo.T").Columns("V", "J").Values(1, json.RawMessage(`{"a":1}`)))
	require.NoError(t, res.Error)
	call := server.only(t)
	require.Len(t, call.args, 2)
	assert.Equal(t, `{"a":1}`, call.args[1])
}

// TestTimesBindInUTC: go-mssqldb sends a time.Time as a datetimeoffset with its own offset,
// and SQL Server keeps the wall clock and drops the offset converting it to datetime2 or
// datetime, EF Core's types for a DateTime. 10:00 in Zurich was stored as 10:00 and read back
// as 10:00 UTC. In UTC the instant survives every one of those types.
func TestTimesBindInUTC(t *testing.T) {
	zurich := time.FixedZone("CEST", 2*60*60)
	local := time.Date(2026, 9, 27, 10, 0, 0, 0, zurich)
	utc := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
	var nilTime *time.Time
	offset := mssql.DateTimeOffset(local)

	bound := bindArgs([]any{local, &local, sql.NullTime{Time: local, Valid: true}, sql.NullTime{}, nilTime, offset})
	assert.Equal(t, utc, bound[0])
	assert.Equal(t, time.UTC, bound[0].(time.Time).Location())
	assert.Equal(t, utc, bound[1], "a *time.Time binds the time it points to")
	assert.Equal(t, sql.NullTime{Time: utc, Valid: true}, bound[2])
	assert.Equal(t, sql.NullTime{}, bound[3], "a NULL stays a NULL")
	assert.Equal(t, nilTime, bound[4])
	assert.Equal(t, offset, bound[5], "mssql.DateTimeOffset keeps its offset on purpose")
	assert.True(t, local.Equal(bound[0].(time.Time)), "the instant is unchanged")

	server := answer(fakeResponse{sets: []fakeSet{tail(1)}})
	res := fakeExecutor(t, server).Exec(ctx, NewBuilder().Update("dbo.T").Set("D2", local).Eq("Id", 1))
	require.NoError(t, res.Error)
	assert.Equal(t, utc, server.only(t).args[0])
}

// ------------------------------------------------------------------- the parameter cap

func ids(n int) []int {
	list := make([]int, n)
	for i := range list {
		list[i] = i
	}
	return list
}

// TestParamLimitCountsExpandedSliceArgs: the dialect counts the arguments it renders, but gorm
// is what turns a slice bound to IN (?) into one parameter each, and a raw statement is not
// rendered by the dialect at all. The callback counts after gorm's expansion, before the
// driver sees anything, instead of letting the server refuse the request with Msg 8003.
func TestParamLimitCountsExpandedSliceArgs(t *testing.T) {
	server := answer(fakeResponse{affected: 1})
	executor := fakeExecutor(t, server)

	res := executor.ExecRaw(ctx, "DELETE FROM [t] WHERE [Id] IN (?)", ids(MaxBindParameters+1))
	requireUnsupported(t, res.Error, fmt.Sprintf("a statement with %d bound parameters", MaxBindParameters+1))
	assert.Empty(t, server.log(), "refused before the wire")

	var rows []map[string]any
	requireUnsupported(t, executor.FindRaw(ctx, &rows, "SELECT 1 FROM [t] WHERE [Id] IN (?)", ids(MaxBindParameters+1)).Error,
		fmt.Sprintf("a statement with %d bound parameters", MaxBindParameters+1))
	_, err := executor.CountRaw(ctx, "SELECT COUNT(*) FROM [t] WHERE [Id] IN (?)", ids(MaxBindParameters+1))
	requireUnsupported(t, err, fmt.Sprintf("a statement with %d bound parameters", MaxBindParameters+1))
	assert.Empty(t, server.log())

	require.NoError(t, executor.ExecRaw(ctx, "DELETE FROM [t] WHERE [Id] IN (?)", ids(MaxBindParameters)).Error)
	assert.Len(t, server.only(t).args, MaxBindParameters, "2098 is still accepted")
}

// TestParamLimitCountsAByteSliceOnce: a []byte is one parameter however long it is.
func TestParamLimitCountsAByteSliceOnce(t *testing.T) {
	server := answer(fakeResponse{affected: 1})

	res := fakeExecutor(t, server).ExecRaw(ctx, "INSERT INTO [t] ([Data]) VALUES (?)", make([]byte, 5000))
	require.NoError(t, res.Error)

	call := server.only(t)
	require.Len(t, call.args, 1)
	assert.Len(t, call.args[0], 5000)
}

// TestParamLimitAppliesToRowAndRows: an app's own db.Raw(…).Row() is capped too, and a refused
// Row() hands back a *sql.Row that reports the refusal instead of nil, on which Scan panics.
func TestParamLimitAppliesToRowAndRows(t *testing.T) {
	server := &fakeServer{}
	db := fakeGorm(t, server, false, false)

	row := db.Raw("SELECT [Id] FROM [t] WHERE [Id] IN (?)", ids(MaxBindParameters+1)).Row()
	require.NotNil(t, row)
	var id int
	requireUnsupported(t, row.Scan(&id), fmt.Sprintf("a statement with %d bound parameters", MaxBindParameters+1))

	rows, err := db.Raw("SELECT [Id] FROM [t] WHERE [Id] IN (?)", ids(MaxBindParameters+1)).Rows()
	requireUnsupported(t, err, fmt.Sprintf("a statement with %d bound parameters", MaxBindParameters+1))
	assert.Nil(t, rows)
	assert.Empty(t, server.log())
}

func TestInstallParamLimitTwiceAddsNothing(t *testing.T) {
	db := fakeGorm(t, &fakeServer{}, false, false)
	require.NoError(t, installParamLimit(db))
	assert.NotNil(t, db.Callback().Raw().Get(paramLimitCallback))
}

// ------------------------------------------------------------------- guards

// TestReadOnlyGuardRefusesRawWrites: the raw methods are not marked as the dialect's, so they
// get the guard's deep check.
func TestReadOnlyGuardRefusesRawWrites(t *testing.T) {
	server := &fakeServer{}
	executor := NewExecutor(fakeGorm(t, server, true, false))

	require.ErrorIs(t, executor.ExecRaw(ctx, "DELETE FROM [t] WHERE [Id] = ?", 1).Error, dbCore.ErrReadOnly)
	var rows []map[string]any
	require.ErrorIs(t, executor.FindRaw(ctx, &rows, "UPDATE [t] SET [a] = 1 OUTPUT INSERTED.[Id]").Error, dbCore.ErrReadOnly)
	_, err := executor.CountRaw(ctx, "SELECT COUNT(*) FROM [t]; DROP TABLE [t]")
	require.ErrorIs(t, err, dbCore.ErrReadOnly)
	assert.Empty(t, server.log())
}

// TestReadOnlyGuardAppliesToBuilderPaths: the dialect of a read-only datasource refuses a
// write where it is built, but a builder made with another dialect is the guard's to refuse.
func TestReadOnlyGuardAppliesToBuilderPaths(t *testing.T) {
	server := &fakeServer{}
	executor := NewExecutor(fakeGorm(t, server, true, false))

	require.ErrorIs(t, executor.Exec(ctx, NewBuilder().Delete("t").Eq("Id", 1)).Error, dbCore.ErrReadOnly)
	require.ErrorIs(t, executor.Exec(ctx, NewBuilder().Delete("t").Eq("Id", 1).Returning("Id")).Error, dbCore.ErrReadOnly)
	var rows []map[string]any
	require.ErrorIs(t, executor.Find(ctx, NewBuilder().Insert("t").Columns("a").Values(1).Returning("Id"), &rows).Error,
		dbCore.ErrReadOnly)
	assert.Empty(t, server.log())
}

// TestReadOnlyAllowsFetchNext: the deny-list matches NEXT VALUE FOR, not NEXT, so a page of a
// read-only datasource runs.
func TestReadOnlyAllowsFetchNext(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{{cols: []string{"Id"}, rows: [][]driver.Value{{int64(6)}}}}})
	executor := NewExecutor(fakeGorm(t, server, true, false))

	var rows []map[string]any
	res := executor.Find(ctx, NewBuilder().Select("*").From("t").OrderBy("Id", "asc").Offset(5).Limit(10), &rows)
	require.NoError(t, res.Error)
	assert.True(t, res.Found)
	assert.Equal(t, "SELECT * FROM [t] ORDER BY [Id] ASC OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY", server.only(t).query)

	_, err := executor.CountRaw(ctx, "SELECT COUNT(*) FROM [t]")
	require.NoError(t, err)
	assert.Len(t, server.statements(), 2)
}

type migratedOrder struct {
	Id     int64
	Status string
}

// TestExternalSchemaGuardRefusesSQLServerMigratorDDL: gorm's SQL Server Migrator renames with
// sp_rename and comments with the extended-property procedures, which are DDL by another
// name.
func TestExternalSchemaGuardRefusesSQLServerMigratorDDL(t *testing.T) {
	server := &fakeServer{}
	db := fakeGorm(t, server, false, true)

	require.ErrorIs(t, db.Migrator().RenameColumn(&migratedOrder{}, "Status", "State"), dbCore.ErrExternalSchema)
	require.ErrorIs(t, db.Migrator().RenameTable("Orders", "Orders2"), dbCore.ErrExternalSchema)
	require.ErrorIs(t, db.Exec("EXEC sp_updateextendedproperty 'MS_Description', N'x', 'SCHEMA', ?, 'TABLE', ?, 'COLUMN', ?",
		"dbo", "Orders", "Status").Error, dbCore.ErrExternalSchema)
	assert.Empty(t, server.log())
}

// TestExternalSchemaAllowsTheWriteBatches: rows may still be written on an external schema,
// and the executor's batches — a write, then a SELECT of its count or key — are not DDL.
func TestExternalSchemaAllowsTheWriteBatches(t *testing.T) {
	server := &fakeServer{respond: func(query string) fakeResponse {
		if strings.Contains(query, "SCOPE_IDENTITY") {
			return fakeResponse{sets: []fakeSet{{cols: []string{"id", "affected"}, rows: [][]driver.Value{{int64(1), int64(1)}}}}}
		}
		return fakeResponse{sets: []fakeSet{tail(1)}}
	}}
	executor := NewExecutor(fakeGorm(t, server, false, true))

	require.NoError(t, executor.Exec(ctx, NewBuilder().Update("dbo.2024Orders").Set("Status", "paid").Eq("Id", 1)).Error)
	require.NoError(t, executor.ExecInsert(ctx, NewBuilder().Insert("dbo.2024Orders").Columns("Status").Values("new")).Error)
	upsert := NewBuilder().Insert("t").Columns("Id", "Name").Values(1, "a").OnConflict("Id").DoUpdate(map[string]any{"Name": "b"})
	require.NoError(t, executor.Exec(ctx, upsert).Error)
	assert.Len(t, server.statements(), 3)
}

// ------------------------------------------------------------------- other

func TestCountReadsFirstColumn(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{{cols: []string{""}, rows: [][]driver.Value{{int64(12)}}}}})

	count, err := fakeExecutor(t, server).Count(ctx, NewBuilder().Select("COUNT(*)").From("dbo.2024Orders"))
	require.NoError(t, err)
	assert.Equal(t, int64(12), count)
	assert.Equal(t, "SELECT COUNT(*) FROM [dbo].[2024Orders]", server.only(t).query)
}

func TestCountPassesRefusalsThrough(t *testing.T) {
	server := &fakeServer{}
	_, err := fakeExecutor(t, server).Count(ctx, NewBuilder().Select("COUNT(*)").From("t").DistinctOn("a"))
	requireUnsupported(t, err, "DISTINCT ON")
	assert.Empty(t, server.log())
}

func TestFindRejectsNilDest(t *testing.T) {
	server := &fakeServer{}
	executor := fakeExecutor(t, server)

	require.Error(t, executor.Find(ctx, NewBuilder().Select("*").From("t"), nil).Error)
	require.Error(t, executor.FindRaw(ctx, nil, "SELECT 1").Error)
	assert.Empty(t, server.log())
}

// TestFindScansOutputRows is how the ORM's Create reads generated columns back: an INSERT
// with Returning is found, not executed.
func TestFindScansOutputRows(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{{cols: []string{"Id", "RowVersion"}, rows: [][]driver.Value{{int64(5), []byte{0, 1}}}}}})

	generated := map[string]any{}
	res := fakeExecutor(t, server).Find(ctx, NewBuilder().Insert("dbo.2024Orders").Columns("Status").Values("new").
		Returning("Id", "RowVersion"), &generated)
	require.NoError(t, res.Error)
	assert.True(t, res.Found)
	assert.Equal(t, int64(5), generated["Id"])
	assert.Equal(t, "INSERT INTO [dbo].[2024Orders] ([Status]) OUTPUT INSERTED.[Id], INSERTED.[RowVersion] VALUES (@p1)",
		server.only(t).query)
}

func TestFindWrapsServerErrors(t *testing.T) {
	server := answer(fakeResponse{err: mssql.Error{Number: 334, Message: "The target table cannot have any enabled triggers"}})

	generated := map[string]any{}
	res := fakeExecutor(t, server).Find(ctx, NewBuilder().Insert("t").Columns("a").Values(1).Returning("Id"), &generated)
	require.Error(t, res.Error)
	assert.Contains(t, res.Error.Error(), "SCOPE_IDENTITY()")
}

// TestServerErrorHints: each number gets its hint, the driver's error stays reachable with
// errors.As, and nothing else is touched.
func TestServerErrorHints(t *testing.T) {
	for number, want := range map[int32]string{
		334:   "ExecInsert",
		544:   "leave the key zero",
		271:   `gorm:"->"`,
		272:   `gorm:"->"`,
		273:   `gorm:"->"`,
		257:   "[]byte",
		8003:  "chunk long IN lists",
		10738: "at most 1000 rows",
		1205:  "safe to run again",
		1033:  "add a Limit",
		8127:  "drop the ORDER BY",
		8155:  "alias each expression",
		8156:  "distinct alias",
		3906:  "primary",
		18456: "auth.tenant_id",
		4060:  "named in db",
		4063:  "named in db",
	} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			err := wrapServerError(fmt.Errorf("query: %w", mssql.Error{Number: number, Message: "server text"}))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "server text")
			assert.Contains(t, err.Error(), want)

			var serverErr mssql.Error
			require.ErrorAs(t, err, &serverErr)
			assert.Equal(t, number, serverErr.Number)
		})
	}

	t.Run("an earlier error in the request", func(t *testing.T) {
		first := mssql.Error{Number: 8003, Message: "too many parameters"}
		err := wrapServerError(mssql.Error{Number: 8180, Message: "statement could not be prepared", All: []mssql.Error{first}})
		assert.Contains(t, err.Error(), "chunk long IN lists")
	})

	t.Run("other errors", func(t *testing.T) {
		assert.NoError(t, wrapServerError(nil))
		plain := errors.New("plain")
		assert.Same(t, plain, wrapServerError(plain))
		unknown := mssql.Error{Number: 2627}
		assert.Equal(t, error(unknown), wrapServerError(unknown))
		refusal := fmt.Errorf("%w: refused", dbCore.ErrReadOnly)
		assert.Same(t, refusal, wrapServerError(refusal))
	})
}
