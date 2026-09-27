package v2

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/osbits/gorgany/v2/db/sql/builder"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// read_only on Postgres, in the order a write meets it: the dialect refuses to render one, the
// guard on the connection refuses one that reaches gorm any other way, and the server is asked
// to make every transaction read-only.
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
// returns both. Only cfg's flags, pool settings and log reach it; nothing is dialled.
func recordingDataSource(t *testing.T, cfg dsconfig.DataSource) (*gormPostgresDataSource, *connRecorder) {
	t.Helper()

	r := &connRecorder{}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(r)}), &gorm.Config{Logger: logger.Discard})
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
func newSession(t *testing.T, ds core.IDataSource) core.ISession {
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

	require.ErrorIs(t, err, core.ErrReadOnly)
	assert.Contains(t, err.Error(), "postgres refuses "+statement+" on a read_only datasource")
	assert.False(t, core.IsUnsupported(err), "a refused write is a policy refusal, not a construct Postgres lacks")
	assert.Empty(t, sql, "no SQL may escape a refusal")
	assert.Nil(t, args)
}

// TestReadOnlyReachesTheDialect: read_only has to reach every builder an app gets from the
// datasource, a transaction's as well as a session's, or a write built on one of them would
// render and be left to the guard.
func TestReadOnlyReachesTheDialect(t *testing.T) {
	ds, r := recordingDataSource(t, readOnlyConfig())
	session := newSession(t, ds)

	assert.Equal(t, &PostgresDialect{ReadOnly: true}, session.Query().Dialect())
	sql, args, err := session.Query().Insert("widgets").Columns("name").Values("a").ToSQL()
	requireReadOnlyRefusal(t, sql, args, err, "INSERT")

	require.NoError(t, session.Transaction(context.Background(), func(tx core.IDBTransaction) error {
		assert.Equal(t, &PostgresDialect{ReadOnly: true}, tx.Query().Dialect())
		sql, args, err := tx.Query().Update("widgets").Set("name", "b").ToSQL()
		requireReadOnlyRefusal(t, sql, args, err, "UPDATE")

		// The transaction is a builder itself, and speaks the same dialect.
		sql, args, err = tx.Delete("widgets").ToSQL()
		requireReadOnlyRefusal(t, sql, args, err, "DELETE")
		return nil
	}))

	assert.Equal(t, []string{"conn1 BEGIN", "conn1 COMMIT"}, r.statements(), "nothing but the transaction may reach the driver")

	owned, _ := recordingDataSource(t, lazyConfig())
	assert.Equal(t, &PostgresDialect{}, owned.Dialect(), "a datasource without the flag renders writes")
}

// TestReadOnlyDialectRefusesEveryWrite: every statement kind the dialect can render that
// writes, refused before anything is rendered, with an error errors.Is finds ErrReadOnly in
// and that names the statement. Each one renders without the flag, so the flag is what
// refuses it.
func TestReadOnlyDialectRefusesEveryWrite(t *testing.T) {
	paid := func(b core.IQueryBuilder) *core.Query {
		return builder.NewLike(b).Select("user_id").From("orders").Eq("status", "paid").Build()
	}

	tests := []struct {
		name      string
		build     func(b core.IQueryBuilder) core.IQueryBuilder
		statement string
	}{
		{"insert", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Insert("widgets").Columns("name").Values("a").Values("b")
		}, "INSERT"},
		{"insert returning", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Insert("widgets").Columns("name").Values("a").Returning("id")
		}, "INSERT"},
		{"insert select", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Insert("archive").Columns("user_id").FromSelect(paid(b))
		}, "INSERT"},
		{"upsert do nothing", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Insert("widgets").Columns("name").Values("a").OnConflict("name").DoNothing()
		}, "INSERT ... ON CONFLICT"},
		{"upsert do update", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Insert("widgets").Columns("name", "label").Values("a", "x").
				OnConflict("name").DoUpdate(map[string]any{"label": "x"})
		}, "INSERT ... ON CONFLICT"},
		{"update", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Update("widgets").Set("label", "x").Eq("id", 1)
		}, "UPDATE"},
		{"update returning", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Update("widgets").Set("label", "x").Returning("id")
		}, "UPDATE"},
		{"delete", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Delete("widgets").Eq("id", 1)
		}, "DELETE"},
		{"delete returning", func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Delete("widgets").Returning("id")
		}, "DELETE"},
		// A data-modifying CTE is a write inside a read. Postgres runs it, so it is refused
		// where the CTE renders.
		{"delete in a CTE", func(b core.IQueryBuilder) core.IQueryBuilder {
			gone := builder.NewLike(b).Delete("widgets").Eq("id", 1).Returning("id").Build()
			return b.WithCTE("gone", gone).Select("id").From("gone")
		}, "DELETE"},
		{"insert in a UNION arm", func(b core.IQueryBuilder) core.IQueryBuilder {
			arm := builder.NewLike(b).Insert("widgets").Columns("name").Values("a").Build()
			return b.Select("id").From("widgets").Union(arm)
		}, "INSERT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered, _, err := tt.build(builder.New(&PostgresDialect{})).ToSQL()
			require.NoError(t, err)
			require.NotEmpty(t, rendered)

			sql, args, err := tt.build(builder.New(&PostgresDialect{ReadOnly: true})).ToSQL()
			requireReadOnlyRefusal(t, sql, args, err, tt.statement)
		})
	}
}

// TestReadOnlySelectStillRenders: the flag changes nothing about a read, RETURNING on a
// SELECT (which Postgres renders without it) and a column named like a write included. The
// whole corpus is checked by TestReadOnlyDialectsRenderEveryReadOfTheGoldenCorpus in
// db/sql/builder.
func TestReadOnlySelectStillRenders(t *testing.T) {
	reads := map[string]func(b core.IQueryBuilder) core.IQueryBuilder{
		"every clause": func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Select("u.id", "COUNT(o.id)").From("users u").
				LeftJoin("orders o", &core.RawCondition{SQL: "o.user_id = u.id"}).
				Eq("u.active", true).GroupBy("u.id").Having(&core.RawCondition{SQL: "COUNT(o.id) > ?", Args: []any{1}}).
				OrderBy("u.id", "desc").Limit(10).Offset(20)
		},
		"cte, union and derived table": func(b core.IQueryBuilder) core.IQueryBuilder {
			paid := builder.NewLike(b).Select("user_id").From("orders").Eq("status", "paid").Build()
			archived := builder.NewLike(b).Select("user_id").From("archived_orders").Build()
			return b.WithCTE("paid", paid).Select("p.user_id").Subquery(paid, "p").Union(archived)
		},
		"returning on a select": func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Select("id").From("widgets").Returning("id")
		},
		"a column named lock": func(b core.IQueryBuilder) core.IQueryBuilder {
			return b.Select("id", "lock").From("locks").Eq("lock", true)
		},
	}

	for name, build := range reads {
		t.Run(name, func(t *testing.T) {
			wantSQL, wantArgs, wantErr := build(builder.New(&PostgresDialect{})).ToSQL()
			require.NoError(t, wantErr)

			sql, args, err := build(builder.New(&PostgresDialect{ReadOnly: true})).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, wantSQL, sql)
			assert.Equal(t, wantArgs, args)
		})
	}
}

// TestPolicyReportsTheReadOnlyFlag: the policy is what the db commands, the provider and the
// session repository ask before they write, so it has to say what the config said. The flag
// is accepted from the config map too, where it used to be refused.
func TestPolicyReportsTheReadOnlyFlag(t *testing.T) {
	ds := newDataSource(t, readOnlyConfig())
	assert.Equal(t, core.DataSourcePolicy{ReadOnly: true}, core.PolicyOf(ds))
	assert.True(t, core.IsReadOnly(ds))
	assert.False(t, core.IsExternalSchema(ds))

	both := readOnlyConfig()
	both.ExternalSchema = true
	assert.Equal(t, core.DataSourcePolicy{ExternalSchema: true, ReadOnly: true}, core.PolicyOf(newDataSource(t, both)))

	fromMap, err := NewDataSource(map[string]any{
		"driver":       "postgres_gorm",
		"host":         "127.0.0.1",
		"port":         1,
		"db":           "Example-db",
		"read_only":    true,
		"lazy_connect": true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, fromMap.Close()) })
	assert.True(t, core.IsReadOnly(fromMap))
}

// TestReadOnlyGuardsTheConnection: a write that does not come from a read-only builder is
// refused by the guard, whichever way it reaches gorm, and never reaches the driver — on the
// handle an app gets from GetDriver, with statement logging on as with it off, and in a
// transaction. ExecInsert, which sends on the statement's pool directly, is refused as it is
// sent.
func TestReadOnlyGuardsTheConnection(t *testing.T) {
	for name, logged := range map[string]bool{"without statement logging": false, "with statement logging": true} {
		t.Run(name, func(t *testing.T) {
			cfg := readOnlyConfig()
			cfg.Log = logged
			ds, r := recordingDataSource(t, cfg)
			db := gormOf(t, ds)
			session := newSession(t, ds)
			ctx := context.Background()

			assert.ErrorIs(t, db.Exec("INSERT INTO widgets (name) VALUES ('a')").Error, core.ErrReadOnly)
			assert.ErrorIs(t, db.Create(&legacyRow{ID: 1}).Error, core.ErrReadOnly)
			assert.ErrorIs(t, session.Executor().ExecRaw(ctx, "DELETE FROM widgets").Error, core.ErrReadOnly)

			// A builder over a dialect that is not read-only renders the write; the guard
			// refuses what it rendered. Exec's is refused in the callback, by the shape check
			// its mark asks for, and ExecInsert's as it is sent on the pool.
			writable := NewBuilder()
			assert.ErrorIs(t, session.Executor().Exec(ctx, writable.Update("widgets").Set("name", "b")).Error, core.ErrReadOnly)
			inserter, ok := session.Executor().(core.LastInsertIDExecutor)
			require.True(t, ok)
			insert := writable.Insert("widgets").Columns("name").Values("a")
			assert.ErrorIs(t, inserter.ExecInsert(ctx, insert).Error, core.ErrReadOnly)

			require.NoError(t, session.Transaction(ctx, func(tx core.IDBTransaction) error {
				assert.ErrorIs(t, tx.ExecRaw(ctx, "UPDATE widgets SET name = 'b'").Error, core.ErrReadOnly)
				txInserter, ok := tx.(core.LastInsertIDExecutor)
				require.True(t, ok)
				assert.ErrorIs(t, txInserter.ExecInsert(ctx, insert).Error, core.ErrReadOnly)
				return nil
			}))

			assert.Equal(t, []string{"conn1 BEGIN", "conn1 COMMIT"}, r.statements(), "no write may reach the driver")
		})
	}
}

// TestBuilderReadsOfKeywordNamedColumnsPassOnReadOnly: the Postgres dialect writes identifiers
// unquoted, so a builder read of a column named lock has LOCK in it, which the deep check
// refuses wherever it stands. The executor marks what a builder rendered, so the guard checks
// its shape instead and the read runs, in a transaction as outside one. The same text sent as
// raw SQL is an app's, and is refused.
func TestBuilderReadsOfKeywordNamedColumnsPassOnReadOnly(t *testing.T) {
	ds, r := recordingDataSource(t, readOnlyConfig())
	session := newSession(t, ds)
	ctx := context.Background()
	read := func() core.IQueryBuilder { return session.Query().Select("id", "lock").From("locks").Eq("lock", true) }

	var rows []map[string]any
	require.NoError(t, session.Executor().Find(ctx, read(), &rows).Error)
	_, err := session.Executor().Count(ctx, session.Query().Select("COUNT(*)").From("locks").Eq("lock", true))
	require.NoError(t, err)
	require.NoError(t, session.Transaction(ctx, func(tx core.IDBTransaction) error {
		return tx.Find(ctx, read(), &rows).Error
	}))

	sent := r.statements()
	require.Len(t, sent, 5, "%q", sent)
	assert.Regexp(t, `^conn\d+ SELECT id, lock FROM locks WHERE lock = \$1$`, sent[0])
	assert.Regexp(t, `^conn\d+ SELECT COUNT\(\*\) FROM locks WHERE lock = \$1$`, sent[1])
	assert.Regexp(t, `^conn\d+ BEGIN$`, sent[2])
	assert.Regexp(t, `^conn\d+ SELECT id, lock FROM locks WHERE lock = \$1$`, sent[3])
	assert.Regexp(t, `^conn\d+ COMMIT$`, sent[4])

	raw, args, err := read().ToSQL()
	require.NoError(t, err)
	assert.ErrorIs(t, session.Executor().FindRaw(ctx, &rows, raw, args...).Error, core.ErrReadOnly)
	_, err = session.Executor().CountRaw(ctx, "SELECT COUNT(*) FROM locks WHERE lock = ?", true)
	assert.ErrorIs(t, err, core.ErrReadOnly)
	assert.Len(t, r.statements(), 5, "raw SQL naming the column must be refused before it is sent")
}

// TestReadOnlyRefusesSetConfig: set_config('default_transaction_read_only', 'off', false) is a
// SELECT, and it switches off the read-only transactions the datasource asks the server for,
// on the pooled connection it runs on, for good. Every write sent on that connection
// afterwards, a nextval() through the guard or an INSERT on DB(), would then pass the server
// too. So it is refused before it reaches the driver, whichever way it is sent: as raw SQL,
// as a raw Select field of a builder read, which the executor marks for the shape check, on
// the handle GetDriver returns, and in a query gorm builds from a model, which only the pool
// checks.
func TestReadOnlyRefusesSetConfig(t *testing.T) {
	const call = "set_config('default_transaction_read_only', 'off', false)"
	ds, r := recordingDataSource(t, readOnlyConfig())
	session := newSession(t, ds)
	db := gormOf(t, ds)
	ctx := context.Background()

	var rows []map[string]any
	assert.ErrorIs(t, session.Executor().FindRaw(ctx, &rows, "SELECT "+call).Error, core.ErrReadOnly)
	assert.ErrorIs(t, session.Executor().FindRaw(ctx, &rows, "SELECT pg_catalog."+call).Error, core.ErrReadOnly)
	assert.ErrorIs(t, session.Executor().Find(ctx, session.Query().Select(call), &rows).Error, core.ErrReadOnly)
	assert.ErrorIs(t, db.Raw("SELECT "+call).Scan(&rows).Error, core.ErrReadOnly)
	var legacy []legacyRow
	assert.ErrorIs(t, db.Model(&legacyRow{}).Select(call).Find(&legacy).Error, core.ErrReadOnly)
	require.NoError(t, session.Transaction(ctx, func(tx core.IDBTransaction) error {
		assert.ErrorIs(t, tx.FindRaw(ctx, &rows, "SELECT "+call).Error, core.ErrReadOnly)
		return nil
	}))

	assert.Equal(t, []string{"conn1 BEGIN", "conn1 COMMIT"}, r.statements(), "no set_config may reach the driver")
}

// TestReadOnlyAddsDefaultTransactionReadOnly: a read_only datasource asks the server to make
// every transaction read-only, so a write that gets past the dialect and the guard — on the
// *sql.DB that DB() returns, say — is refused by the server. pgx forwards the key as a runtime
// parameter, which is how it reaches the server.
func TestReadOnlyAddsDefaultTransactionReadOnly(t *testing.T) {
	cfg := baseConfig()
	cfg.ReadOnly = true
	cfg.SearchPath = "reports"
	cfg.Options = map[string]string{"application_name": "reporting", "connect_timeout": "5"}

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Equal(t,
		"host=localhost port=5432 user=gorgany password=secret dbname=gorgany_test sslmode=disable "+
			"search_path=reports default_transaction_read_only=on application_name=reporting connect_timeout=5",
		dsn)

	parsed, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	assert.Equal(t, "on", parsed.RuntimeParams["default_transaction_read_only"])

	cfg.ReadOnly = false
	dsn, err = BuildDSN(cfg)
	require.NoError(t, err)
	assert.NotContains(t, dsn, "default_transaction_read_only", "a writable datasource's DSN is unchanged")
}

// TestReadOnlyAsksForReadOnlyTransactionsWithSimpleProtocol: prefer_simple_protocol chooses
// the wire protocol, not a proxy, so it does not leave the parameter out. A datasource behind a
// PgBouncer that refuses the parameter says so itself, by setting the option empty (see
// TestReadOnlyKeepsAnExplicitDefaultTransactionReadOnlyOption).
func TestReadOnlyAsksForReadOnlyTransactionsWithSimpleProtocol(t *testing.T) {
	cfg := baseConfig()
	cfg.ReadOnly = true
	cfg.PreferSimpleProtocol = true

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Equal(t, "host=localhost port=5432 user=gorgany password=secret dbname=gorgany_test sslmode=disable default_transaction_read_only=on", dsn)
}

// TestReadOnlyKeepsAnExplicitDefaultTransactionReadOnlyOption: an operator who set the
// parameter has said what the server should do, off included, whether as a key of its own, in
// any case, or with -c or -- in libpq's options key, spelt any way Postgres reads as the same
// setting there. The parameter BuildDSN would add is sent after options and overrides them, so
// missing a spelling would override the operator. An empty key of its own is how an operator
// sends nothing at all, as behind a PgBouncer that refuses the parameter; an empty options key
// sets nothing, so it does not count.
func TestReadOnlyKeepsAnExplicitDefaultTransactionReadOnlyOption(t *testing.T) {
	const base = "host=localhost port=5432 user=gorgany password=secret dbname=gorgany_test sslmode=disable"
	tests := map[string]struct {
		options map[string]string
		want    string
	}{
		"its own key": {
			map[string]string{"default_transaction_read_only": "off"},
			" default_transaction_read_only=off",
		},
		"its own key, in another case": {
			map[string]string{"Default_Transaction_Read_Only": "off"},
			" Default_Transaction_Read_Only=off",
		},
		"its own key, empty, to send nothing": {
			map[string]string{"default_transaction_read_only": ""},
			"",
		},
		"libpq's options key": {
			map[string]string{"options": "-c default_transaction_read_only=off"},
			` options='-c default_transaction_read_only=off'`,
		},
		"libpq's options key, attached to -c": {
			map[string]string{"options": "-cdefault_transaction_read_only=off"},
			` options=-cdefault_transaction_read_only=off`,
		},
		"libpq's options key, with dashes": {
			map[string]string{"options": "-c default-transaction-read-only=off"},
			` options='-c default-transaction-read-only=off'`,
		},
		"libpq's options key, as a long switch": {
			map[string]string{"options": "--default-transaction-read-only=off"},
			` options=--default-transaction-read-only=off`,
		},
		"libpq's options key, in another case": {
			map[string]string{"options": "-c DEFAULT_TRANSACTION_READ_ONLY=off"},
			` options='-c DEFAULT_TRANSACTION_READ_ONLY=off'`,
		},
		"libpq's options key, with an escaped character": {
			map[string]string{"options": `-c default_transaction\_read_only=off`},
			` options='-c default_transaction\\_read_only=off'`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.ReadOnly = true
			cfg.Options = tt.options

			dsn, err := BuildDSN(cfg)
			require.NoError(t, err)
			assert.Equal(t, base+tt.want, dsn)

			assert.NotContains(t, dsn, "default_transaction_read_only=on", "nothing may be added to what the operator set")
		})
	}

	t.Run("an empty options key", func(t *testing.T) {
		cfg := baseConfig()
		cfg.ReadOnly = true
		cfg.Options = map[string]string{"options": ""}

		dsn, err := BuildDSN(cfg)
		require.NoError(t, err)
		assert.Equal(t, base+" default_transaction_read_only=on", dsn)
	})

	t.Run("options that name another setting", func(t *testing.T) {
		cfg := baseConfig()
		cfg.ReadOnly = true
		cfg.Options = map[string]string{"options": "-c statement_timeout=5000"}

		dsn, err := BuildDSN(cfg)
		require.NoError(t, err)
		assert.Contains(t, dsn, " default_transaction_read_only=on ")
	})
}

// TestReadOnlySkipsDefaultTransactionReadOnlyForTargetSessionAttrs: pgx picks a host for
// target_session_attrs read-write or read-only by asking the new session's
// transaction_read_only, which the parameter turns on for every host. read-write would find no
// host, and read-only would take the primary as readily as a standby. standby asks
// pg_is_in_recovery(), which the parameter does not change, so it keeps it.
func TestReadOnlySkipsDefaultTransactionReadOnlyForTargetSessionAttrs(t *testing.T) {
	for attrs, asked := range map[string]bool{"read-write": false, "read-only": false, "standby": true, "any": true} {
		t.Run(attrs, func(t *testing.T) {
			cfg := baseConfig()
			cfg.ReadOnly = true
			cfg.Options = map[string]string{"target_session_attrs": attrs}

			dsn, err := BuildDSN(cfg)
			require.NoError(t, err)
			if asked {
				assert.Contains(t, dsn, " default_transaction_read_only=on ")
			} else {
				assert.NotContains(t, dsn, "default_transaction_read_only")
			}
		})
	}
}
