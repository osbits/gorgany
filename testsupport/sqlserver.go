package testsupport

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"gorm.io/gorm"
)

// What the harness does differently on SQL Server: it creates the test database, and it empties
// tables with a sequence of its own. Both are written against the framework's datasource and
// gorm, never against the engine's package, since testsupport must not link SQL Server's driver
// into every app's test binary; DriverSQLServer, a string, is how the harness knows the engine.

// ---------------------------------------------------------------- database creation

// createsItsDatabase reports whether the harness creates database's database when it is
// missing: on SQL Server, unless the host is Azure SQL's.
//
// The Postgres and MySQL images create the database named in their environment, so their
// containers start with gorgany_test in place. SQL Server's image has no such setting, so without
// this every developer, and every CI job, would need a step that creates the database through
// master before the first test. On Azure SQL, creating a database provisions a billable one with
// a service tier of its own, which is never what a test run means; there the database has to be
// created by whoever owns the server. The target guard refuses an Azure SQL host anyway unless
// AllowAnyTarget is set.
func createsItsDatabase(database DatabaseConfig) bool {
	return database.Driver == DriverSQLServer && !dsconfig.IsAzureSQLHost(database.Host)
}

// sqlServerCreatableName is the database name the harness will put into a CREATE DATABASE.
//
// The statement cannot take the name as a parameter, so it is written into the SQL. The name is
// bracketed and escaped as well, but a name of letters, digits and underscores needs neither,
// and a statement built from one cannot be anything but the statement it looks like. A database
// with any other name can still be used, once somebody has created it.
var sqlServerCreatableName = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)

// sqlServerCreateDatabaseSQL returns the batch that creates the database called name if it does
// not exist, or an error when name is not one the harness creates (see sqlServerCreatableName).
//
// A database the batch creates gets READ_COMMITTED_SNAPSHOT ON, the setting every Azure SQL
// database has and an on-premises one lacks by default. With it, a read sees the last committed
// version of a row instead of waiting for the transaction that holds it, which is how Postgres
// and MySQL read too: without it, a test under IsolateByRollback that reads a table through
// another connection, as Gorm() is, waits for the test's own transaction, which ends only when
// the test does. It is set only on a database the batch has just created, both because nobody
// else can be connected to it then, which the setting requires, and because how an existing
// database reads is its owner's decision, not the harness's.
func sqlServerCreateDatabaseSQL(name string) (string, error) {
	if !sqlServerCreatableName.MatchString(name) {
		return "", fmt.Errorf("the harness creates only a SQL Server database named with letters, "+
			"digits and underscores, so it will not create %q; create it yourself, or rename it", name)
	}
	quoted := quoteSQLServerIdentifier(name)
	return "IF DB_ID(" + sqlServerStringLiteral(name) + ") IS NULL BEGIN " +
		"CREATE DATABASE " + quoted + "; " +
		"ALTER DATABASE " + quoted + " SET READ_COMMITTED_SNAPSHOT ON; END", nil
}

// sqlServerBootstrapConfig is target pointed at master, the database a SQL Server login can
// reach before its own database exists.
//
// It is a copy of target, which has already passed guardTarget, so it reaches the same server
// with the same credentials and TLS settings. It is not passed through guardTarget again, which
// would refuse it: master is a system database, and the guard refuses emptying one. The harness
// never empties master; it runs one CREATE DATABASE there, for a name the guard accepted.
func sqlServerBootstrapConfig(target DatabaseConfig) DatabaseConfig {
	bootstrap := target
	bootstrap.Database = "master"
	return bootstrap
}

// ensureSQLServerDatabase creates target's database through master if it does not exist; see
// sqlServerCreateDatabaseSQL. open builds the datasource, and is driver.New outside the tests.
//
// A second process creating the same database at the same moment makes one of the two fail
// with "database already exists". waitForEngine retries it, and the retry finds the database.
func ensureSQLServerDatabase(target DatabaseConfig, open func(dsconfig.DataSource) (dbCore.IDataSource, error)) (err error) {
	statement, err := sqlServerCreateDatabaseSQL(target.Database)
	if err != nil {
		return err
	}

	parsed, err := dsconfig.Parse(sqlServerBootstrapConfig(target).datasourceConfig())
	if err != nil {
		return err
	}
	master, err := open(parsed)
	if err != nil {
		return fmt.Errorf("connecting to master to create database %s: %w", target.Database, err)
	}
	defer func() { err = errors.Join(err, master.Close()) }()

	gormDb, err := gormOf(master)
	if err != nil {
		return err
	}
	if err := gormDb.Exec(statement).Error; err != nil {
		return fmt.Errorf("creating database %s through master: %w", target.Database, err)
	}
	return nil
}

// sqlServerDatabaseExists reports whether target's database exists, asking master, the database
// a login can reach whether or not its own exists. open builds the datasource, as for
// ensureSQLServerDatabase.
//
// The name is bound as an argument rather than written into the SQL, so unlike CREATE DATABASE
// this takes any name. DB_ID answers NULL for a database the login may not see as well as for
// one that does not exist, which is why waitForEngine asks only after connecting to the
// database itself has failed.
func sqlServerDatabaseExists(target DatabaseConfig, open func(dsconfig.DataSource) (dbCore.IDataSource, error)) (exists bool, err error) {
	parsed, err := dsconfig.Parse(sqlServerBootstrapConfig(target).datasourceConfig())
	if err != nil {
		return false, err
	}
	master, err := open(parsed)
	if err != nil {
		return false, fmt.Errorf("connecting to master to look for database %s: %w", target.Database, err)
	}
	defer func() { err = errors.Join(err, master.Close()) }()

	gormDb, err := gormOf(master)
	if err != nil {
		return false, err
	}
	var id sql.NullInt64
	if err := gormDb.Raw("SELECT DB_ID(?)", target.Database).Row().Scan(&id); err != nil {
		return false, fmt.Errorf("looking for database %s through master: %w", target.Database, err)
	}
	return id.Valid, nil
}

// gormOf returns datasource's *gorm.DB, which is what the harness runs its own statements on.
func gormOf(datasource dbCore.IDataSource) (*gorm.DB, error) {
	raw, err := datasource.GetDriver()
	if err != nil {
		return nil, err
	}
	gormDb, ok := raw.(*gorm.DB)
	if !ok {
		return nil, fmt.Errorf("driver is %T, not *gorm.DB", raw)
	}
	return gormDb, nil
}

// ---------------------------------------------------------------- truncation

// sqlServerTable is one table SQL Server truncation empties.
type sqlServerTable struct {
	name string

	// reseedTo is what DBCC CHECKIDENT reseeds the table's identity to, so that the next row
	// gets the identity's seed, as it would in a table nobody had written to: the seed less the
	// increment, 0 for the usual IDENTITY(1,1).
	//
	// It is empty when the table has no identity, which DBCC CHECKIDENT refuses (Msg 7997), and
	// when its identity has never issued a value. An identity that has not issued one hands the
	// next row the reseed value itself rather than the value after it, so reseeding it would
	// hand out 0, and it needs no reseeding anyway.
	reseedTo string
}

// sqlServerTruncation is the statements that empty a set of tables on SQL Server, in three
// groups that run in order.
type sqlServerTruncation struct {
	// disable switches off each table's foreign key and check constraints, so that rows can be
	// deleted in any order. The switch is a change to the table, not to the session, so it
	// outlives the connection that makes it.
	disable []string

	// empty deletes every row, then reseeds the identities that need it.
	empty []string

	// restore switches each table's constraints back on WITH CHECK, one per statement in
	// disable, in the same order. WITH CHECK is what marks a foreign key trusted again: without
	// it the constraint is enforced for new rows, but the optimizer stops relying on it.
	restore []string
}

// sqlServerReseedValue is the form a reseed value must have to be written into DBCC CHECKIDENT.
// The value comes from the server's own catalog, but it is still written into SQL.
var sqlServerReseedValue = regexp.MustCompile(`^-?[0-9]{1,40}$`)

// sqlServerTruncateStatements returns the statements that empty tables, which are in the order
// their rows are deleted.
//
// DELETE rather than TRUNCATE TABLE, which SQL Server refuses on a table any foreign key
// references, even a disabled one, so it could not empty a parent table at all. DELETE does not
// restart an identity, so the identities that have issued a value are reseeded afterwards,
// which is what Postgres's RESTART IDENTITY and MySQL's TRUNCATE do.
func sqlServerTruncateStatements(tables []sqlServerTable) (sqlServerTruncation, error) {
	var plan sqlServerTruncation
	for _, table := range tables {
		quoted := quoteSQLServerIdentifier(table.name)
		plan.disable = append(plan.disable, "ALTER TABLE "+quoted+" NOCHECK CONSTRAINT ALL")
		plan.empty = append(plan.empty, "DELETE FROM "+quoted)
		plan.restore = append(plan.restore, "ALTER TABLE "+quoted+" WITH CHECK CHECK CONSTRAINT ALL")
	}
	for _, table := range tables {
		if table.reseedTo == "" {
			continue
		}
		if !sqlServerReseedValue.MatchString(table.reseedTo) {
			return sqlServerTruncation{}, fmt.Errorf("cannot reseed %s to %q, which is not an integer",
				table.name, table.reseedTo)
		}
		plan.empty = append(plan.empty, "DBCC CHECKIDENT ("+
			sqlServerStringLiteral(quoteSQLServerIdentifier(table.name))+", RESEED, "+table.reseedTo+
			") WITH NO_INFOMSGS")
	}
	return plan, nil
}

// truncateSQLServer empties tables, given in the order their rows are deleted.
//
// SQL Server has no TRUNCATE ... CASCADE and no session switch for foreign keys, so the tables'
// constraints are switched off, their rows deleted, their identities reseeded, and their
// constraints switched back on. The last step runs whatever happened before it, and its failure
// is reported, even after an earlier one: the switch outlives the session, so a table left with
// its constraints off would let every later test in every later run write rows its foreign keys
// forbid.
//
// Truncations on one engine run one at a time. Restoring a child table's foreign key reads the
// parent while holding the child, and deleting from a parent whose child's key another test has
// just restored reads the child while holding the parent, so two tests truncating at once, under
// t.Parallel, could deadlock, and SQL Server would fail one of them.
//
// named says the caller chose tables, rather than the harness emptying every table the
// migrations created. A name that is not a table in this database is then an error, as it is on
// Postgres and MySQL. Among the migrations' tables it is skipped instead: gorm lists views
// alongside tables on SQL Server, and a view the migrations created has no rows of its own.
func (d *Database) truncateSQLServer(tables []string, named bool) (err error) {
	if d.truncating != nil {
		d.truncating.Lock()
		defer d.truncating.Unlock()
	}

	found, err := d.sqlServerTables(tables, named)
	if err != nil {
		return err
	}
	plan, err := sqlServerTruncateStatements(found)
	if err != nil {
		return err
	}

	disabled := 0
	defer func() {
		for _, statement := range plan.restore[:disabled] {
			if restoreErr := d.gorm.Exec(statement).Error; restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restoring constraints, which stay off until "+
					"this succeeds: %s: %w", statement, restoreErr))
			}
		}
	}()

	for _, statement := range plan.disable {
		if err := d.gorm.Exec(statement).Error; err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
		disabled++
	}
	for _, statement := range plan.empty {
		if err := d.gorm.Exec(statement).Error; err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

// sqlServerTables looks names up in sys.tables, in order, with what reseeding each one needs;
// see sqlServerTable.
//
// Each name resolves the way an unqualified name in a statement does, in the login's default
// schema, and bracketed, so a name with a dot in it is one name. OBJECT_ID's type U is a user
// table, which leaves out views.
func (d *Database) sqlServerTables(names []string, named bool) ([]sqlServerTable, error) {
	values := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		values[i] = fmt.Sprintf("(%d, OBJECT_ID(?, N'U'))", i)
		args[i] = quoteSQLServerIdentifier(name)
	}

	query := "SELECT v.position, CASE WHEN ic.last_value IS NULL THEN NULL ELSE " +
		"CAST(CAST(ic.seed_value AS decimal(38, 0)) - CAST(ic.increment_value AS decimal(38, 0)) AS varchar(40)) END " +
		"FROM (VALUES " + strings.Join(values, ", ") + ") AS v(position, object_id) " +
		"JOIN sys.tables AS t ON t.object_id = v.object_id " +
		"LEFT JOIN sys.identity_columns AS ic ON ic.object_id = t.object_id"

	rows, err := d.gorm.Raw(query, args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("looking up the tables to empty: %w", err)
	}
	defer func() { _ = rows.Close() }()

	reseed := make(map[int]string, len(names))
	for rows.Next() {
		var position int
		var reseedTo sql.NullString
		if err := rows.Scan(&position, &reseedTo); err != nil {
			return nil, fmt.Errorf("looking up the tables to empty: %w", err)
		}
		reseed[position] = reseedTo.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("looking up the tables to empty: %w", err)
	}

	tables := make([]sqlServerTable, 0, len(names))
	for i, name := range names {
		reseedTo, ok := reseed[i]
		if !ok {
			if named {
				return nil, fmt.Errorf("%s is not a table in this database", name)
			}
			continue
		}
		tables = append(tables, sqlServerTable{name: name, reseedTo: reseedTo})
	}
	return tables, nil
}

// quoteSQLServerIdentifier brackets name, doubling any closing bracket in it, which is the one
// character a bracketed identifier escapes.
func quoteSQLServerIdentifier(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// sqlServerStringLiteral renders s as a Unicode string literal, doubling any quote in it.
func sqlServerStringLiteral(s string) string {
	return "N'" + strings.ReplaceAll(s, "'", "''") + "'"
}
