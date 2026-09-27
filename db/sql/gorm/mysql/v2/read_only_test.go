package v2

import (
	"context"
	"database/sql"
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// read_only on MySQL, in the order a write meets it: the dialect refuses to render one, and the
// guard on the connection refuses one that reaches gorm any other way. Unlike Postgres, the
// DSN asks the server for nothing; TestReadOnlyAddsNothingToTheDSN says why.
//
// The datasources here are finished on a gorm handle over connRecorder (executor_tx_test.go)
// with fromOpened, the code NewDataSourceWithConfig finishes its own with, so a test sees
// every statement that reached the driver: a refusal that left the log empty was made before
// anything was sent.

// readOnlyConfig is a read_only config for a server nobody runs.
func readOnlyConfig() dsconfig.DataSource {
	cfg := lazyConfig()
	cfg.ReadOnly = true
	return cfg
}

// recordingDataSource finishes a datasource for cfg on a handle over a connRecorder, and
// returns both. Only cfg's flags, pool settings and log reach it; nothing is dialled, and the
// driver is not asked its version.
func recordingDataSource(t *testing.T, cfg dsconfig.DataSource) (*gormMySQLDataSource, *connRecorder) {
	t.Helper()

	r := &connRecorder{}
	dialector := mysql.New(mysql.Config{Conn: sql.OpenDB(r), SkipInitializeWithVersion: true})
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); assert.NoError(t, err) {
			assert.NoError(t, sqlDB.Close())
		}
	})

	ds, err := fromOpened(db, cfg)
	require.NoError(t, err)
	return ds, r
}

// newSession opens a session on ds.
func newSession(t *testing.T, ds *gormMySQLDataSource) dbCore.ISession {
	t.Helper()

	session, err := ds.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()) })
	return session
}

// requireReadOnlyRefusal asserts err is the dialect's refusal of statement, rendered as
// nothing at all.
func requireReadOnlyRefusal(t *testing.T, sql string, args []any, err error, statement string) {
	t.Helper()

	require.ErrorIs(t, err, dbCore.ErrReadOnly)
	assert.Contains(t, err.Error(), "mysql refuses "+statement+" on a read_only datasource")
	assert.False(t, dbCore.IsUnsupported(err), "a refused write is a policy refusal, not a construct MySQL lacks")
	assert.Empty(t, sql, "no SQL may escape a refusal")
	assert.Nil(t, args)
}

// TestReadOnlyReachesTheDialect: read_only has to reach every builder an app gets from the
// datasource, a transaction's as well as a session's, or a write built on one of them would
// render and be left to the guard. It travels with the upsert opt-in, and does not replace it.
func TestReadOnlyReachesTheDialect(t *testing.T) {
	cfg := readOnlyConfig()
	cfg.AllowUnfaithfulUpsert = true
	ds, r := recordingDataSource(t, cfg)
	session := newSession(t, ds)

	want := &MySQLDialect{AllowUnfaithfulUpsert: true, ReadOnly: true}
	assert.Equal(t, want, session.Query().Dialect())
	sql, args, err := session.Query().Insert("widgets").Columns("name").Values("a").ToSQL()
	requireReadOnlyRefusal(t, sql, args, err, "INSERT")

	require.NoError(t, session.Transaction(context.Background(), func(tx dbCore.IDBTransaction) error {
		assert.Equal(t, want, tx.Query().Dialect())
		sql, args, err := tx.Query().Update("widgets").Set("name", "b").ToSQL()
		requireReadOnlyRefusal(t, sql, args, err, "UPDATE")

		// The transaction is a builder itself, and speaks the same dialect.
		sql, args, err = tx.Delete("widgets").ToSQL()
		requireReadOnlyRefusal(t, sql, args, err, "DELETE")
		return nil
	}))

	assert.Equal(t, []string{"conn1 BEGIN", "conn1 COMMIT"}, r.statements(), "nothing but the transaction may reach the driver")

	owned, _ := recordingDataSource(t, lazyConfig())
	assert.Equal(t, &MySQLDialect{}, owned.Dialect(), "a datasource without the flag renders writes")
}

// TestReadOnlyDialectRefusesEveryWrite: every statement kind the dialect can render that
// writes, refused before anything is rendered, with an error errors.Is finds ErrReadOnly in
// and that names the statement, whether or not the upsert opt-in is set. Each one renders
// without the flag, so the flag is what refuses it.
func TestReadOnlyDialectRefusesEveryWrite(t *testing.T) {
	paid := func(b dbCore.IQueryBuilder) *dbCore.Query {
		return builder.NewLike(b).Select("user_id").From("orders").Eq("status", "paid").Build()
	}

	tests := []struct {
		name      string
		build     func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder
		statement string
	}{
		{"insert", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("widgets").Columns("name").Values("a").Values("b")
		}, "INSERT"},
		{"insert select", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("archive").Columns("user_id").FromSelect(paid(b))
		}, "INSERT"},
		{"upsert do nothing", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("widgets").Columns("name").Values("a").OnConflict("name").DoNothing()
		}, "INSERT ... ON CONFLICT"},
		{"upsert do update", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("widgets").Columns("name", "label").Values("a", "x").
				OnConflict("name").DoUpdate(map[string]any{"label": "x"})
		}, "INSERT ... ON CONFLICT"},
		{"update", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("widgets").Set("label", "x").Eq("id", 1)
		}, "UPDATE"},
		{"delete", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("widgets").Eq("id", 1)
		}, "DELETE"},
		{"delete in a CTE", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			gone := builder.NewLike(b).Delete("widgets").Eq("id", 1).Build()
			return b.WithCTE("gone", gone).Select("id").From("gone")
		}, "DELETE"},
		{"insert in a UNION arm", func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			arm := builder.NewLike(b).Insert("widgets").Columns("name").Values("a").Build()
			return b.Select("id").From("widgets").Union(arm)
		}, "INSERT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered, _, err := tt.build(builder.New(&MySQLDialect{AllowUnfaithfulUpsert: true})).ToSQL()
			require.NoError(t, err)
			require.NotEmpty(t, rendered)

			for _, dialect := range []*MySQLDialect{{ReadOnly: true}, {ReadOnly: true, AllowUnfaithfulUpsert: true}} {
				sql, args, err := tt.build(builder.New(dialect)).ToSQL()
				requireReadOnlyRefusal(t, sql, args, err, tt.statement)
			}
		})
	}

	// A write MySQL could not render anyway is refused for being a write: no rewrite makes it
	// runnable on a read-only datasource, so that is the refusal worth reading.
	t.Run("a write MySQL cannot express either", func(t *testing.T) {
		for statement, build := range map[string]func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder{
			"INSERT": func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return b.Insert("widgets").Columns("name").Values("a").Returning("id")
			},
			"DELETE": func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return b.Delete("widgets").Returning("id")
			},
			"INSERT ... ON CONFLICT": func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return b.Insert("widgets").Columns("name").Values("a").OnConflict("name").DoUpdate(map[string]any{"name": "a"})
			},
		} {
			_, _, err := build(builder.New(&MySQLDialect{})).ToSQL()
			require.Truef(t, dbCore.IsUnsupported(err), "%s must be unsupported without the flag: %v", statement, err)

			sql, args, err := build(builder.New(&MySQLDialect{ReadOnly: true})).ToSQL()
			requireReadOnlyRefusal(t, sql, args, err, statement)
		}
	})
}

// TestReadOnlySelectStillRenders: the flag changes nothing about a read, refusals included —
// RETURNING on a SELECT is still MySQL's refusal, not the flag's — and a column named like a
// write renders as it does without it. db/sql/builder's
// TestReadOnlyDialectsRenderEveryReadOfTheGoldenCorpus checks the whole corpus.
func TestReadOnlySelectStillRenders(t *testing.T) {
	reads := map[string]func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder{
		"every clause": func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("u.id", "COUNT(o.id)").From("users u").
				LeftJoin("orders o", &dbCore.RawCondition{SQL: "o.user_id = u.id"}).
				Eq("u.active", true).GroupBy("u.id").Having(&dbCore.RawCondition{SQL: "COUNT(o.id) > ?", Args: []any{1}}).
				OrderBy("u.id", "desc").Limit(10).Offset(20)
		},
		"cte, union and derived table": func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			paid := builder.NewLike(b).Select("user_id").From("orders").Eq("status", "paid").Build()
			archived := builder.NewLike(b).Select("user_id").From("archived_orders").Build()
			return b.WithCTE("paid", paid).Select("p.user_id").Subquery(paid, "p").Union(archived)
		},
		"a column named copy": func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id", "copy").From("copies").Eq("copy", true)
		},
	}

	for name, build := range reads {
		t.Run(name, func(t *testing.T) {
			wantSQL, wantArgs, wantErr := build(builder.New(&MySQLDialect{})).ToSQL()
			require.NoError(t, wantErr)

			sql, args, err := build(builder.New(&MySQLDialect{ReadOnly: true})).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, wantSQL, sql)
			assert.Equal(t, wantArgs, args)
		})
	}

	t.Run("returning on a select", func(t *testing.T) {
		_, _, err := builder.New(&MySQLDialect{ReadOnly: true}).Select("id").From("widgets").Returning("id").ToSQL()
		requireUnsupported(t, err, "RETURNING")
		assert.NotErrorIs(t, err, dbCore.ErrReadOnly)
	})
}

// TestPolicyReportsTheReadOnlyFlag: the policy is what the db commands, the provider and the
// session repository ask before they write, so it has to say what the config said. The flag
// is accepted from the config map too, where it used to be refused.
func TestPolicyReportsTheReadOnlyFlag(t *testing.T) {
	ds := newDataSource(t, readOnlyConfig())
	assert.Equal(t, dbCore.DataSourcePolicy{ReadOnly: true}, dbCore.PolicyOf(ds))
	assert.True(t, dbCore.IsReadOnly(ds))
	assert.False(t, dbCore.IsExternalSchema(ds))

	both := readOnlyConfig()
	both.ExternalSchema = true
	assert.Equal(t, dbCore.DataSourcePolicy{ExternalSchema: true, ReadOnly: true}, dbCore.PolicyOf(newDataSource(t, both)))

	fromMap, err := NewDataSource(map[string]any{
		"driver":       "mysql_gorm",
		"host":         "127.0.0.1",
		"port":         1,
		"db":           "Example-db",
		"read_only":    true,
		"lazy_connect": true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, fromMap.Close()) })
	assert.True(t, dbCore.IsReadOnly(fromMap))
}

// TestReadOnlyGuardsTheConnection: a write that does not come from a read-only builder is
// refused by the guard, whichever way it reaches gorm, and never reaches the driver — on the
// handle an app gets from GetDriver, with statement logging on as with it off, and in a
// transaction. ExecInsert, which the ORM's Create takes on MySQL and which sends on the
// statement's pool directly, is refused as it is sent.
func TestReadOnlyGuardsTheConnection(t *testing.T) {
	for name, logged := range map[string]bool{"without statement logging": false, "with statement logging": true} {
		t.Run(name, func(t *testing.T) {
			cfg := readOnlyConfig()
			cfg.Log = logged
			ds, r := recordingDataSource(t, cfg)
			db := gormOf(t, ds)
			session := newSession(t, ds)
			ctx := context.Background()

			assert.ErrorIs(t, db.Exec("INSERT INTO widgets (name) VALUES ('a')").Error, dbCore.ErrReadOnly)
			assert.ErrorIs(t, db.Create(&legacyRow{ID: 1}).Error, dbCore.ErrReadOnly)
			assert.ErrorIs(t, session.Executor().ExecRaw(ctx, "DELETE FROM widgets").Error, dbCore.ErrReadOnly)
			// MySQL runs the text of a /*! … */ comment, and the MySQL lexicon knows it.
			assert.ErrorIs(t, session.Executor().ExecRaw(ctx, "SELECT 1 /*! ; DELETE FROM widgets */").Error, dbCore.ErrReadOnly)

			// A builder over a dialect that is not read-only renders the write; the guard
			// refuses what it rendered. Exec's is refused in the callback, by the shape check
			// its mark asks for, and ExecInsert's as it is sent on the pool.
			writable := NewBuilder()
			assert.ErrorIs(t, session.Executor().Exec(ctx, writable.Update("widgets").Set("name", "b")).Error, dbCore.ErrReadOnly)
			inserter, ok := session.Executor().(dbCore.LastInsertIDExecutor)
			require.True(t, ok)
			insert := writable.Insert("widgets").Columns("name").Values("a")
			assert.ErrorIs(t, inserter.ExecInsert(ctx, insert).Error, dbCore.ErrReadOnly)

			require.NoError(t, session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
				assert.ErrorIs(t, tx.ExecRaw(ctx, "UPDATE widgets SET name = 'b'").Error, dbCore.ErrReadOnly)
				txInserter, ok := tx.(dbCore.LastInsertIDExecutor)
				require.True(t, ok)
				assert.ErrorIs(t, txInserter.ExecInsert(ctx, insert).Error, dbCore.ErrReadOnly)
				return nil
			}))

			assert.Equal(t, []string{"conn1 BEGIN", "conn1 COMMIT"}, r.statements(), "no write may reach the driver")
		})
	}
}

// TestBuilderReadsOfKeywordNamedColumnsPassOnReadOnly: the MySQL dialect writes identifiers
// unquoted, so a builder read of a column named copy has COPY in it, which the deep check
// refuses wherever it stands. The executor marks what a builder rendered, so the guard checks
// its shape instead and the read runs, in a transaction as outside one. The same text sent as
// raw SQL is an app's, and is refused.
func TestBuilderReadsOfKeywordNamedColumnsPassOnReadOnly(t *testing.T) {
	ds, r := recordingDataSource(t, readOnlyConfig())
	session := newSession(t, ds)
	ctx := context.Background()
	read := func() dbCore.IQueryBuilder {
		return session.Query().Select("id", "copy").From("copies").Eq("copy", true)
	}

	var rows []map[string]any
	require.NoError(t, session.Executor().Find(ctx, read(), &rows).Error)
	_, err := session.Executor().Count(ctx, session.Query().Select("COUNT(*)").From("copies").Eq("copy", true))
	require.NoError(t, err)
	require.NoError(t, session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
		return tx.Find(ctx, read(), &rows).Error
	}))

	sent := r.statements()
	require.Len(t, sent, 5, "%q", sent)
	assert.Regexp(t, `^conn\d+ SELECT id, copy FROM copies WHERE copy = \?$`, sent[0])
	assert.Regexp(t, `^conn\d+ SELECT COUNT\(\*\) FROM copies WHERE copy = \?$`, sent[1])
	assert.Regexp(t, `^conn\d+ BEGIN$`, sent[2])
	assert.Regexp(t, `^conn\d+ SELECT id, copy FROM copies WHERE copy = \?$`, sent[3])
	assert.Regexp(t, `^conn\d+ COMMIT$`, sent[4])

	raw, args, err := read().ToSQL()
	require.NoError(t, err)
	assert.ErrorIs(t, session.Executor().FindRaw(ctx, &rows, raw, args...).Error, dbCore.ErrReadOnly)
	_, err = session.Executor().CountRaw(ctx, "SELECT COUNT(*) FROM copies WHERE copy = ?", true)
	assert.ErrorIs(t, err, dbCore.ErrReadOnly)
	assert.Len(t, r.statements(), 5, "raw SQL naming the column must be refused before it is sent")
}

// TestReadOnlyAddsNothingToTheDSN: MySQL's read-only session variable is spelled
// transaction_read_only on MySQL 8 and tx_read_only on MySQL 5.7 before 5.7.20 and MariaDB
// before 11.1, and go-sql-driver/mysql sends an unknown DSN parameter as a SET that fails the
// connection when the server does not know the name. No one spelling is safe, so read_only
// adds none, and an operator who knows the server sets the one it has under options.
func TestReadOnlyAddsNothingToTheDSN(t *testing.T) {
	cfg := mysqlConfig()
	writable, err := BuildDSN(cfg)
	require.NoError(t, err)

	cfg.ReadOnly = true
	readOnly, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Equal(t, writable, readOnly)

	cfg.Options = map[string]string{"transaction_read_only": "1"}
	explicit, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, explicit, "&transaction_read_only=1", "an option naming the server's variable passes through")
}
