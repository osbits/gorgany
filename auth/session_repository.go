package auth

import (
	"context"
	"fmt"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

type ISessionRepository interface {
	FindById(id string) (*DbSessionEntity, error)
	// Save persists the session. An implementation must be handed a detached copy, never
	// a session the mediator shares between requests — see DbSessionEntity.Snapshot.
	Save(session *DbSessionEntity) error
	Delete(session *DbSessionEntity) error
	// DeleteById removes the row for id and reports whether there was one. An absent row
	// is not an error: revocation is idempotent, and the caller needs the boolean to tell
	// "revoked it" from "there was nothing to revoke" — session rotation refuses to carry
	// a user id over from a session that has already been revoked.
	DeleteById(id string) (bool, error)
	DeleteExpired() error
}

type DbSessionRepository struct {
	dbContext core.IDBContext `container:"inject"`
}

func NewDbSessionRepository() *DbSessionRepository {
	return &DbSessionRepository{}
}

// sessionAccess is what an operation does to the sessions table, which decides whether the
// default datasource's policy lets it run at all. See refuseAccess.
type sessionAccess int

const (
	readsSessions sessionAccess = iota
	writesSessions
)

// refuseAccess reports why dataSource's policy forbids access, or nil when it allows it.
//
// It asks core.PolicyOf rather than anything reachable through a session, and is consulted
// before a session is opened, so a refused operation sends nothing to the database: no
// statement, and no connection taken from the pool to find out.
//
// On an external_schema default even reads are refused. gorgany never creates the sessions
// table there — db:migrate leaves its migrations out — so a table of that name belongs
// to whoever owns the schema, and reading its rows as gorgany sessions would let foreign data
// decide who is logged in. A read_only default refuses only writes: its sessions table, on a
// replica of a database gorgany owns, is gorgany's own, and looking a session up changes
// nothing.
//
// The provider already stops the boot for auth.session.storage: database on either kind of
// default. This applies the same rule to a repository an app wires itself, and to a default
// registered on the DBContext after that check ran.
//
// When both flags are set the refusal names external_schema, as every policy refusal does
// (see core.DataSourcePolicy.Refusal), and reads are refused with it.
func refuseAccess(dataSource dbCore.IDataSource, access sessionAccess) error {
	refusal := dbCore.PolicyOf(dataSource).Refusal()
	if refusal == nil || (refusal == dbCore.ErrReadOnly && access == readsSessions) {
		return nil
	}
	return fmt.Errorf("session storage refuses datasource %q: %w", core.DefaultKeyInRegistrar, refusal)
}

// defaultDataSource resolves the datasource an operation of the given kind may use.
func (r *DbSessionRepository) defaultDataSource(access sessionAccess) (dbCore.IDataSource, error) {
	dataSource := r.dbContext.GetDataSource(core.DefaultKeyInRegistrar)
	if dataSource == nil {
		return nil, fmt.Errorf("no data source available")
	}
	if err := refuseAccess(dataSource, access); err != nil {
		return nil, err
	}
	return dataSource, nil
}

func (r *DbSessionRepository) withOrm(access sessionAccess, operation func(*orm.ORM[*DbSessionEntity]) error) error {
	dataSource, err := r.defaultDataSource(access)
	if err != nil {
		return err
	}

	dbSession, err := dataSource.NewSession()
	if err != nil {
		return err
	}
	defer dbSession.Close()

	orm := orm.New[*DbSessionEntity](dbSession)
	return operation(orm)
}

func (r *DbSessionRepository) FindById(id string) (*DbSessionEntity, error) {
	var session *DbSessionEntity
	err := r.withOrm(readsSessions, func(orm *orm.ORM[*DbSessionEntity]) error {
		var findErr error
		session, findErr = orm.Find(id)
		return findErr
	})
	return session, err
}

// Save persists the session, and refuses to call an update against a row that is gone a
// success.
//
// orm.Save would have used the plain update, which reports only the statement's own error.
// An UPDATE keyed on a primary key that no longer exists matches nothing and succeeds, so
// a session whose row had been deleted — by a logout on this process or on another one —
// was written "successfully" and every caller above believed the state was persisted.
// UpdateExisting resolves that: it checks the row when the statement matched none, so a
// no-op update of a live row still succeeds while a write with nowhere to land does not.
func (r *DbSessionRepository) Save(session *DbSessionEntity) error {
	return r.withOrm(writesSessions, func(o *orm.ORM[*DbSessionEntity]) error {
		if meta := session.GetMeta(); meta != nil && meta.IsLoaded {
			return o.UpdateExisting(session)
		}
		return o.Create(session)
	})
}

func (r *DbSessionRepository) Delete(session *DbSessionEntity) error {
	return r.withOrm(writesSessions, func(orm *orm.ORM[*DbSessionEntity]) error {
		return orm.Delete(session)
	})
}

// DeleteById removes the row in one statement.
//
// It used to read the row and then delete the entity it found, which made an already-absent
// session an error: FindById returns nil, and orm.Delete(nil) answers "domain cannot be
// nil". Revoking a session twice — a double-clicked logout, a retried request, a row the
// sweep already collected — therefore failed, and because the mediator returned before
// purging its cache, the failure left the cache entry the delete was supposed to revoke
// still serving requests. Deleting is idempotent, so zero rows removed is success, and the
// count is reported so a caller that needs to know whether the session was live can ask.
func (r *DbSessionRepository) DeleteById(id string) (bool, error) {
	if id == "" {
		return false, nil
	}

	deleted := false
	err := r.withSession(writesSessions, func(session dbCore.ISession, _ string) error {
		builder := session.Query().Delete((&DbSessionEntity{}).TableName()).
			Where(&dbCore.BinaryCondition{Left: "id", Operator: "=", Right: id})

		result := session.Executor().Exec(context.Background(), builder)
		if result.Error != nil {
			return result.Error
		}

		deleted = result.RowsAffected > 0
		return nil
	})

	return deleted, err
}

// SessionSweepBatchSize is how many rows one DELETE removes. See DeleteExpired.
//
// A variable rather than a constant so an operator with an unusual table can change it. The
// default is a compromise: small enough that one statement is short, large enough that a
// backlog of millions clears in a bounded number of round trips.
var SessionSweepBatchSize = 1000

// SessionSweepMaxBatches caps one sweep, so it cannot run forever if rows arrive as fast as
// they are deleted. At the default batch size this is 10 million rows per sweep; whatever is
// left waits for the next tick, which is reported rather than silently dropped.
var SessionSweepMaxBatches = 10_000

// BatchedExpiredDeleteSQL deletes at most one batch of expired sessions, on Postgres and MySQL.
//
// The nested derived table is not redundant. MySQL rejects a subquery selecting from the same
// table as the DELETE with error 1093, "You can't specify target table for update in FROM
// clause", and wrapping it in a second SELECT is the documented way round that; Postgres
// accepts the same statement unchanged. Both were run against live Postgres 16 and MySQL 8.4
// before this shipped, deleting exactly the batch size each time.
//
// `DELETE ... LIMIT n` would be shorter and is MySQL-only — Postgres has no LIMIT on DELETE —
// so one statement serving both engines had to be this one. SQL Server accepts neither form,
// and has its own (BatchedExpiredDeleteSQLServer); BatchedExpiredDeleteSQLFor picks between
// them, and this is what it returns for every dialect but SQL Server, so an engine the
// repository cannot name keeps the statement it always ran.
//
// It is exported for two reasons: the portability check in e2e has to run this exact
// statement against both live engines, and an app that prefers to sweep from its own
// migration or cron can reuse it rather than writing a fourth version of it.
const BatchedExpiredDeleteSQL = `DELETE FROM sessions WHERE id IN ` +
	`(SELECT id FROM (SELECT id FROM sessions WHERE expiry < NOW() LIMIT ?) AS batch)`

// BatchedExpiredDeleteSQLServer is BatchedExpiredDeleteSQL for SQL Server.
//
// T-SQL has neither LIMIT nor NOW(), so the portable statement fails there as a syntax
// error before it deletes anything. TOP bounds a DELETE directly, which also makes MySQL's
// derived-table detour unnecessary; which expired rows a batch takes is arbitrary, and for a
// sweep that is fine, since every one of them is going. The bound is a parameter, and TOP
// takes one only in parentheses.
//
// The cut-off is SYSDATETIMEOFFSET(), not GETDATE(). The sessions migration gives expiry the
// type datetimeoffset on SQL Server, and GETDATE() is a datetime in the server's local time
// with no offset: comparing the two treats that local time as UTC, so on a server whose
// clock is not on UTC the sweep would delete sessions hours early, or keep them hours late.
// SYSDATETIMEOFFSET() carries its offset, so the comparison is between instants.
const BatchedExpiredDeleteSQLServer = `DELETE TOP (?) FROM [sessions] WHERE [expiry] < SYSDATETIMEOFFSET()`

// sqlServerDialect is the name SQL Server's dialect reports (SQLDialect.Name()).
//
// It is compared as a string rather than taken from the SQL Server engine's package, because
// importing that package would link go-mssqldb into every app that uses database sessions,
// whether or not it ever speaks SQL Server.
const sqlServerDialect = "sqlserver"

// BatchedExpiredDeleteSQLFor returns the batched sweep statement for the dialect named
// dialect: BatchedExpiredDeleteSQLServer for "sqlserver", and BatchedExpiredDeleteSQL for any
// other name, the empty one included.
//
// Both take the batch size as their only argument, so a caller sweeping by hand runs either
// the same way.
func BatchedExpiredDeleteSQLFor(dialect string) string {
	if dialect == sqlServerDialect {
		return BatchedExpiredDeleteSQLServer
	}
	return BatchedExpiredDeleteSQL
}

// DeleteExpired removes expired sessions in batches.
//
// It used to be one statement: `DELETE FROM sessions WHERE expiry < NOW()`. That was fine
// while nothing called it, which was the situation until H4 — the job meant to call it was
// registered by nothing. H4 gives it a caller, and the first sweep on an app that has been
// running for months therefore has to delete everything accumulated since deployment in a
// single statement: a long lock on the matched tuples, a WAL burst proportional to the whole
// backlog, and bloat that then needs VACUUM. The fix for the leak would have caused an
// outage on precisely the apps that had leaked the most.
//
// Batching keeps each statement short and lets other transactions interleave. There is no
// enclosing transaction on purpose: the point is that each batch commits on its own, so a
// sweep interrupted halfway keeps the work it already did.
//
// The statement is BatchedExpiredDeleteSQLFor the dialect the default datasource reports, so
// a SQL Server default gets the T-SQL one. A datasource that does not report a dialect gets
// the Postgres and MySQL statement, as every datasource did before SQL Server could be one.
//
// Each batch runs with context.Background(), and neither ISessionRepository nor
// core.ISessionStorage takes a context, so stopping the scheduler does not interrupt a sweep
// that is under way: it ends when the expired rows run out or at SessionSweepMaxBatches. A
// long backlog can therefore hold a graceful shutdown until its deadline. Passing the job's
// context down would need a context-taking variant at every layer between the job and here,
// which is left for a change of its own rather than added to one of them.
func (r *DbSessionRepository) DeleteExpired() error {
	return r.withSession(writesSessions, func(session dbCore.ISession, dialect string) error {
		statement := BatchedExpiredDeleteSQLFor(dialect)

		batch := SessionSweepBatchSize
		if batch <= 0 {
			batch = 1000
		}

		for i := 0; i < SessionSweepMaxBatches; i++ {
			result := session.Executor().ExecRaw(context.Background(), statement, batch)
			if result.Error != nil {
				return result.Error
			}

			// A short batch means the last expired row is gone. Checking the count is what
			// makes this terminate without a second query per iteration — ExecRaw reports
			// RowsAffected, which RawQuery (returning an entity) does not.
			if result.RowsAffected < int64(batch) {
				return nil
			}
		}

		return fmt.Errorf(
			"sessions sweep stopped after %d batches of %d; expired rows remain and will be "+
				"collected on the next run", SessionSweepMaxBatches, batch)
	})
}

// withSession hands over the session itself, for an operation that needs the executor rather
// than the ORM — here, the RowsAffected that makes batching terminate — together with the
// name of the datasource's dialect (see dialectNameOf), for an operation whose SQL is not the
// same on every engine.
func (r *DbSessionRepository) withSession(access sessionAccess, operation func(session dbCore.ISession, dialect string) error) error {
	dataSource, err := r.defaultDataSource(access)
	if err != nil {
		return err
	}

	dbSession, err := dataSource.NewSession()
	if err != nil {
		return err
	}
	defer dbSession.Close()

	return operation(dbSession, dialectNameOf(dataSource))
}

// dialectNameOf returns the name of the dialect dataSource reports, or "" when it reports
// none.
//
// It asks the datasource rather than the session. The Postgres, MySQL and SQL Server
// datasources all have a Dialect method, but IDataSource does not declare it, so a datasource
// an app has written may not; and a session's dialect is reachable only through a builder,
// which asking for would open more than the question needs. A datasource that has the method
// and returns a nil dialect is treated as reporting none.
func dialectNameOf(dataSource dbCore.IDataSource) string {
	reporter, ok := dataSource.(interface{ Dialect() dbCore.SQLDialect })
	if !ok {
		return ""
	}
	dialect := reporter.Dialect()
	if dialect == nil {
		return ""
	}
	return dialect.Name()
}
