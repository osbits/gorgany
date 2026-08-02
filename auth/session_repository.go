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

func (r *DbSessionRepository) withOrm(operation func(*orm.ORM[*DbSessionEntity]) error) error {
	dataSource := r.dbContext.GetDataSource(core.DefaultKeyInRegistrar)
	if dataSource == nil {
		return fmt.Errorf("no data source available")
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
	err := r.withOrm(func(orm *orm.ORM[*DbSessionEntity]) error {
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
	return r.withOrm(func(o *orm.ORM[*DbSessionEntity]) error {
		if meta := session.GetMeta(); meta != nil && meta.IsLoaded {
			return o.UpdateExisting(session)
		}
		return o.Create(session)
	})
}

func (r *DbSessionRepository) Delete(session *DbSessionEntity) error {
	return r.withOrm(func(orm *orm.ORM[*DbSessionEntity]) error {
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
	err := r.withSession(func(session dbCore.ISession) error {
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

// BatchedExpiredDeleteSQL deletes at most one batch of expired sessions.
//
// The nested derived table is not redundant. MySQL rejects a subquery selecting from the same
// table as the DELETE with error 1093, "You can't specify target table for update in FROM
// clause", and wrapping it in a second SELECT is the documented way round that; Postgres
// accepts the same statement unchanged. Both were run against live Postgres 16 and MySQL 8.4
// before this shipped, deleting exactly the batch size each time.
//
// `DELETE ... LIMIT n` would be shorter and is MySQL-only — Postgres has no LIMIT on DELETE —
// so the two engines would have needed different SQL and this repository has no dialect.
// It is exported for two reasons: the portability check in e2e has to run this exact
// statement against both live engines, and an app that prefers to sweep from its own
// migration or cron can reuse it rather than writing a fourth version of it.
const BatchedExpiredDeleteSQL = `DELETE FROM sessions WHERE id IN ` +
	`(SELECT id FROM (SELECT id FROM sessions WHERE expiry < NOW() LIMIT ?) AS batch)`

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
func (r *DbSessionRepository) DeleteExpired() error {
	return r.withSession(func(session dbCore.ISession) error {
		batch := SessionSweepBatchSize
		if batch <= 0 {
			batch = 1000
		}

		for i := 0; i < SessionSweepMaxBatches; i++ {
			result := session.Executor().ExecRaw(
				context.Background(), BatchedExpiredDeleteSQL, batch)
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
// than the ORM — here, the RowsAffected that makes batching terminate.
func (r *DbSessionRepository) withSession(operation func(dbCore.ISession) error) error {
	dataSource := r.dbContext.GetDataSource(core.DefaultKeyInRegistrar)
	if dataSource == nil {
		return fmt.Errorf("no data source available")
	}

	dbSession, err := dataSource.NewSession()
	if err != nil {
		return err
	}
	defer dbSession.Close()

	return operation(dbSession)
}
