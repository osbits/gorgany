package auth

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"
	"github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	pgv2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
)

// The repository keeps sessions on `default`, so its policy decides what the repository may do
// there. external_schema refuses every operation, since the sessions table there would be
// someone else's; read_only refuses the writes and still lets a session be looked up. A refusal
// must happen before anything reaches the database, so these doubles count sessions opened and
// every executor call rather than embedding nil interfaces: an operation that got further than
// it should is a failed count, not a nil-pointer panic that says nothing about why.

// ---------------------------------------------------------------- stub plumbing

// recordingExecutor counts every call and answers each with an empty success.
type recordingExecutor struct {
	calls   int
	findRaw int
	lastSQL string
}

func (e *recordingExecutor) Exec(context.Context, dbCore.IQueryBuilder) dbCore.QueryResult {
	e.calls++
	return dbCore.QueryResult{}
}

func (e *recordingExecutor) Find(context.Context, dbCore.IQueryBuilder, interface{}) dbCore.QueryResult {
	e.calls++
	return dbCore.QueryResult{}
}

func (e *recordingExecutor) Count(context.Context, dbCore.IQueryBuilder) (int64, error) {
	e.calls++
	return 0, nil
}

func (e *recordingExecutor) ExecRaw(_ context.Context, sql string, _ ...interface{}) dbCore.QueryResult {
	e.calls++
	e.lastSQL = sql
	return dbCore.QueryResult{}
}

func (e *recordingExecutor) FindRaw(_ context.Context, _ interface{}, sql string, _ ...interface{}) dbCore.QueryResult {
	e.calls++
	e.findRaw++
	e.lastSQL = sql
	return dbCore.QueryResult{}
}

func (e *recordingExecutor) CountRaw(_ context.Context, sql string, _ ...interface{}) (int64, error) {
	e.calls++
	e.lastSQL = sql
	return 0, nil
}

// policySession is a whole ISession over the recording executor. Its builder is a real one, so
// an operation the policy allows renders its SQL exactly as it would against the engine.
type policySession struct {
	dataSource *policyDataSource
}

func (s *policySession) Executor() dbCore.IQueryExecutor { return s.dataSource.executor }
func (s *policySession) Query() dbCore.IQueryBuilder {
	return builder.New(&pgv2.PostgresDialect{})
}
func (s *policySession) Transaction(context.Context, func(dbCore.IDBTransaction) error) error {
	s.dataSource.executor.calls++
	return nil
}
func (s *policySession) DataSource() dbCore.IDataSource { return s.dataSource }
func (s *policySession) Close() error                   { return nil }

// policyDataSource reports a fixed policy, as a configured external_schema or read_only
// datasource does, and counts the sessions opened on it.
type policyDataSource struct {
	policy   dbCore.DataSourcePolicy
	executor *recordingExecutor
	opened   int
}

func (d *policyDataSource) NewSession() (dbCore.ISession, error) {
	d.opened++
	return &policySession{dataSource: d}, nil
}
func (d *policyDataSource) GetDriver() (any, error)         { return nil, nil }
func (d *policyDataSource) Close() error                    { return nil }
func (d *policyDataSource) Policy() dbCore.DataSourcePolicy { return d.policy }

// policyRepository wires a repository whose `default` reports policy.
func policyRepository(policy dbCore.DataSourcePolicy) (*DbSessionRepository, *policyDataSource) {
	dataSource := &policyDataSource{policy: policy, executor: &recordingExecutor{}}
	return &DbSessionRepository{dbContext: &stubSweepDbContext{dataSource: dataSource}}, dataSource
}

// repositoryOperation is one ISessionRepository method, classified by whether it writes.
type repositoryOperation struct {
	method string
	writes bool
	run    func(*DbSessionRepository) error
}

func everyRepositoryOperation() []repositoryOperation {
	return []repositoryOperation{
		{"FindById", false, func(r *DbSessionRepository) error {
			_, err := r.FindById("s-1")
			return err
		}},
		{"Save", true, func(r *DbSessionRepository) error {
			return r.Save(&DbSessionEntity{ID: "s-1", UserID: "user@example.com"})
		}},
		{"Save", true, func(r *DbSessionRepository) error {
			// A loaded session takes the update path rather than the insert.
			return r.Save(&DbSessionEntity{ID: "s-1", Meta: orm.EntityMeta{IsLoaded: true}})
		}},
		{"Delete", true, func(r *DbSessionRepository) error {
			return r.Delete(&DbSessionEntity{ID: "s-1"})
		}},
		{"DeleteById", true, func(r *DbSessionRepository) error {
			_, err := r.DeleteById("s-1")
			return err
		}},
		{"DeleteExpired", true, func(r *DbSessionRepository) error {
			return r.DeleteExpired()
		}},
	}
}

// -------------------------------------------------------------------- the tests

// TestEveryRepositoryOperationIsClassified keeps the list above complete. A method added to
// ISessionRepository and missing here would go untested by the policy tests below, and the
// question those tests ask — does it read or write? — is the one a new method must answer.
func TestEveryRepositoryOperationIsClassified(t *testing.T) {
	classified := map[string]bool{}
	for _, operation := range everyRepositoryOperation() {
		classified[operation.method] = true
	}

	repository := reflect.TypeOf((*ISessionRepository)(nil)).Elem()
	for i := 0; i < repository.NumMethod(); i++ {
		assert.Truef(t, classified[repository.Method(i).Name],
			"ISessionRepository.%s is not in everyRepositoryOperation", repository.Method(i).Name)
	}
}

// TestTheRepositoryRefusesEveryOperationOnAnExternalSchemaDefault. gorgany never creates the
// sessions table on an external_schema default, so a table by that name belongs to whoever owns
// the schema: reading it would let their rows decide who is logged in, and writing it would
// change their data. read_only on top changes nothing; external_schema is the reason given.
func TestTheRepositoryRefusesEveryOperationOnAnExternalSchemaDefault(t *testing.T) {
	for name, policy := range map[string]dbCore.DataSourcePolicy{
		"external_schema":             {ExternalSchema: true},
		"external_schema & read_only": {ExternalSchema: true, ReadOnly: true},
	} {
		t.Run(name, func(t *testing.T) {
			for _, operation := range everyRepositoryOperation() {
				repo, dataSource := policyRepository(policy)

				err := operation.run(repo)

				require.ErrorIsf(t, err, dbCore.ErrExternalSchema, "%s must be refused", operation.method)
				assert.NotErrorIsf(t, err, dbCore.ErrReadOnly,
					"%s: the schema is the reason, whatever else is set", operation.method)
				assert.Containsf(t, err.Error(), `session storage refuses datasource "default"`,
					"%s: the refusal has to name the datasource", operation.method)
				assert.Zerof(t, dataSource.opened, "%s must not open a session", operation.method)
				assert.Zerof(t, dataSource.executor.calls, "%s must not reach the executor", operation.method)
			}
		})
	}
}

// TestTheRepositoryRefusesWritesOnAReadOnlyDefaultButStillReads. A read_only default's sessions
// table is gorgany's own — on a replica, say — so looking a session up is allowed; creating,
// updating, revoking and sweeping are not.
func TestTheRepositoryRefusesWritesOnAReadOnlyDefaultButStillReads(t *testing.T) {
	for _, operation := range everyRepositoryOperation() {
		if !operation.writes {
			continue
		}
		repo, dataSource := policyRepository(dbCore.DataSourcePolicy{ReadOnly: true})

		err := operation.run(repo)

		require.ErrorIsf(t, err, dbCore.ErrReadOnly, "%s must be refused", operation.method)
		assert.Containsf(t, err.Error(), `session storage refuses datasource "default"`,
			"%s: the refusal has to name the datasource", operation.method)
		assert.Zerof(t, dataSource.opened, "%s must not open a session", operation.method)
		assert.Zerof(t, dataSource.executor.calls, "%s must not reach the executor", operation.method)
	}

	repo, dataSource := policyRepository(dbCore.DataSourcePolicy{ReadOnly: true})

	session, err := repo.FindById("s-1")

	require.NoError(t, err, "a read_only default must still answer a lookup")
	assert.Nil(t, session, "the stub finds no row")
	assert.Equal(t, 1, dataSource.executor.findRaw, "the lookup must reach the database")
	assert.Equal(t, 1, dataSource.executor.calls, "and nothing else may")
	assert.Contains(t, dataSource.executor.lastSQL, "sessions")
}

// TestAnOwnedDefaultIsNotRefused is the control: the zero policy is what every datasource
// reported before the flags existed, and it must reach the executor for writes as well.
func TestAnOwnedDefaultIsNotRefused(t *testing.T) {
	repo, dataSource := policyRepository(dbCore.DataSourcePolicy{})

	deleted, err := repo.DeleteById("s-1")
	require.NoError(t, err)
	assert.False(t, deleted, "the stub reports no row affected")

	require.NoError(t, repo.DeleteExpired())

	assert.Equal(t, 2, dataSource.opened)
	assert.Equal(t, 2, dataSource.executor.calls)
	assert.Equal(t, BatchedExpiredDeleteSQL, dataSource.executor.lastSQL,
		"the sweep statement is unchanged")
}

// TestAPolicyRefusalIsTheSentinelsText pins the whole message, since it is what an operator
// reads in the log of a failed login.
func TestAPolicyRefusalIsTheSentinelsText(t *testing.T) {
	repo, _ := policyRepository(dbCore.DataSourcePolicy{ReadOnly: true})

	assert.EqualError(t, repo.DeleteExpired(),
		`session storage refuses datasource "`+core.DefaultKeyInRegistrar+`": `+dbCore.ErrReadOnly.Error())
}
