# SQL dialects

The query builder is engine-agnostic. It accumulates clauses into a
`dbCore.Query` and delegates every byte of SQL rendering to an injected
`dbCore.SQLDialect`. This document explains how to implement a dialect for
another engine, and records exactly what MySQL and SQL Server refuse, and why.

**Before v2 none of this was reachable.** `NewBuilder()` hard-coded
`&PostgresDialect{}`, and the `Config{Dialect}` struct that looked like the
intended seam was never constructed or read by anything. The only way to reach
another engine was to fork the whole 886-line builder.

---

## The seam

```
db/sql/config      typed datasource configuration, engine-independent
db/sql/core        interfaces (SQLDialect, IQueryBuilder, Query, conditions), the
                   condition rendering seam (RenderContext), optional dialect
                   capabilities, datasource policy and the SQL guards
db/sql/builder     the engine-agnostic Builder — no SQL syntax of its own
db/sql/driver      driver-name -> constructor registry
db/sql/driver/builtin     registers postgres_gorm and mysql_gorm
db/sql/driver/sqlserver   registers sqlserver_gorm; not part of builtin
db/sql/driver/sqlserver/azuread  registers the Entra ID sign-in methods, and the driver
db/sql/driver/sqlserver/azuread/persistentcache  the keychain token cache (development)
db/sql/gorm/guard         gorm callbacks enforcing read_only and external_schema
db/sql/gorm/postgres/v2   PostgresDialect + Postgres datasource
db/sql/gorm/mysql/v2      MySQLDialect + MySQL datasource
db/sql/gorm/sqlserver/v2  SQLServerDialect + SQL Server / Azure SQL datasource
```

The dialect is threaded **datasource → session → builder**:

```go
// The datasource declares which dialect its connection speaks.
func (ds *gormMySQLDataSource) Dialect() dbCore.SQLDialect { return &MySQLDialect{} }

// The session carries it.
func (ds *gormMySQLDataSource) NewSession() (core.ISession, error) {
    return &sessionImpl{executor: NewExecutor(session), dialect: ds.Dialect(), dataSource: ds}, nil
}

// Every builder the session produces already speaks the right dialect.
func (s *sessionImpl) Query() core.IQueryBuilder { return builder.New(s.dialect) }
```

So application code never picks a dialect. It asks the session for a builder and
gets one wired to the connection it will run against.

To build a builder directly:

```go
b := builder.New(&MySQLDialect{})
b := builder.NewFromConfig(builder.Config{Dialect: &MySQLDialect{}})
b := mysqlv2.NewBuilderWithDialect(myDialect)   // per-engine convenience
```

`builder.New(nil)` panics at construction with a precise message. A nil dialect is
a programming error, not a configuration one: there is no sensible default —
picking one silently is how the Postgres dialect got welded in — and the
alternative is a nil dereference later, inside `ToSQL`, far from the call site.

---

## Implementing another dialect

### 1. Implement `dbCore.SQLDialect`

Eighteen methods. `Name()` and `QuoteIdentifier()` are the two that are purely
yours; the sixteen `Format*` methods each render one clause and return
`(string, []any, error)`.

Implementations must be **stateless and safe for concurrent use**: one dialect
value is shared by every builder created from a datasource.

```go
package sqlite

import (
    "fmt"
    "strings"

    dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

const DialectName = "sqlite"

type SQLiteDialect struct{}

// Fail the build if a method is missing or mis-typed.
var _ dbCore.SQLDialect = (*SQLiteDialect)(nil)

func (d *SQLiteDialect) Name() string { return DialectName }

func (d *SQLiteDialect) QuoteIdentifier(field string) string {
    parts := strings.Split(field, ".")
    for i, p := range parts {
        parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
    }
    return strings.Join(parts, ".")
}

func unsupported(construct, hint string) error {
    return dbCore.Unsupported(DialectName, construct, hint)
}

// SQLite has no FULL OUTER JOIN before 3.39.
func (d *SQLiteDialect) FormatJoin(join *dbCore.JoinClause) (string, []any, error) {
    if strings.EqualFold(strings.TrimSpace(join.Type), "FULL") {
        return "", nil, unsupported("FULL OUTER JOIN",
            "requires SQLite 3.39+, or emulate with LEFT JOIN unioned with RIGHT JOIN")
    }
    ...
}
```

### 2. Refuse what you cannot express

**The governing rule: never emit SQL the server will reject.** If your engine
cannot express a construct, return a `dbCore.UnsupportedError` naming it and
pointing at the portable alternative.

```go
func (d *SQLiteDialect) FormatReturning(fields []string) (string, []any, error) {
    return "", nil, unsupported("RETURNING",
        "requires SQLite 3.35+; before that, SELECT the row after the write")
}
```

Callers can branch on it:

```go
sql, args, err := b.ToSQL()
if err != nil {
    var e *dbCore.UnsupportedError
    if errors.As(err, &e) {
        log.Printf("%s cannot express %s: %s", e.Dialect, e.Construct, e.Hint)
    }
    return err
}
```

**Propagate refusals from nested positions.** A construct can appear inside a CTE,
a `FROM` subquery, a joined subquery, a `UNION` arm, or an `INSERT ... SELECT`.
Every one of those paths must return the error rather than swallowing it into
partially rendered SQL:

```go
func (d *SQLiteDialect) FormatCTE(name string, query *dbCore.Query) (string, []any, error) {
    sql, args, err := d.FormatQuery(query)
    if err != nil {
        return "", nil, err        // do not build a string around a failure
    }
    return fmt.Sprintf("%s AS (%s)", name, sql), args, nil
}
```

On error, return an **empty** SQL string. Callers rely on it: no half-rendered SQL
may escape.

### 3. Keep output deterministic

`UPDATE ... SET` and `ON CONFLICT DO UPDATE SET` are generated from a
`map[string]any`. Ranging a Go map yields a different column order on every call,
which does not corrupt results — arguments are appended in the same pass — but it
makes the SQL untestable and defeats any statement cache keyed on the query text.
Use `dbCore.SortedKeys`:

```go
for _, field := range dbCore.SortedKeys(q.Update.Values) {
    updates = append(updates, field+" = ?")
    args = append(args, q.Update.Values[field])
}
```

### 4. Harden the ORDER BY slot

`ORDER BY` takes an identifier, not a bound value, so it is the one place a
request-derived string could reach the SQL text. The Postgres and MySQL dialects
use the same three-way rule, and another dialect should too:

```go
func (d *SQLiteDialect) orderByField(f dbCore.OrderByField) (string, []any) {
    direction := normalizeOrderDirection(f.Direction)   // whitelist to ASC/DESC

    if f.Raw {                                          // trusted caller expression
        return fmt.Sprintf("%s %s", f.Field, direction), nil
    }
    if simpleIdentifier.MatchString(f.Field) {          // quote a real identifier
        return fmt.Sprintf("%s %s", d.QuoteIdentifier(f.Field), direction), nil
    }
    // Anything else is untrusted data: BIND it rather than interpolate. This
    // degrades to a constant sort key — a harmless no-op sort — but the value can
    // never be executed as SQL.
    return fmt.Sprintf("? %s", direction), []any{f.Field}
}
```

`Builder.OrderBy` is the untrusted entry point; `Builder.OrderByRaw` is the
trusted one. Never route request input through `OrderByRaw`.

The SQL Server dialect refuses the third case instead of binding it. SQL Server
answers a constant `ORDER BY` item with Msg 1008, so binding would only move the
failure to the server, and on the other engines the bound value sorts by a
constant, so the `ORDER BY` silently does nothing.

### 5. Never emit an empty clause

`FormatGroupBy` used to read only `GroupByClause.Fields`, so a query built with
`Rollup()` and no plain `GroupBy()` emitted a bare `GROUP BY ` — a syntax error.
Return `("", nil, nil)` when a clause has nothing to render, and let
`formatSelect` skip it.

### 6. Register the driver

```go
package mydrivers

import (
    dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
    dbCore "github.com/osbits/gorgany/v2/db/sql/core"
    "github.com/osbits/gorgany/v2/db/sql/driver"
)

func init() {
    driver.Register("sqlite_gorm", func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
        return sqlite.NewDataSourceWithConfig(cfg)
    })
}
```

Import the package for its side effect from your app's provider, then name it in
config:

```yaml
databases:
  default:
    driver: sqlite_gorm
    host: localhost
    db: app.db
```

Registering a name twice panics: two drivers answering to one config value is a
wiring mistake with no correct resolution, and silently keeping one of them is
how the pre-v2 provider ended up nondeterministic. An unknown driver name is a
boot error listing the names that are registered.

A config that sets `external_schema: true` or `read_only: true` boots only when the
datasource your constructor returns reports the flag, by implementing
`core.PolicyReporter`. Only the engine can enforce either one, by refusing DDL or
writes on its own connection, so `driver.New` closes a datasource that does not
report a flag its config sets and fails the boot with a `core.UnsupportedError`
naming the driver and the key. A constructor written before the flags existed
therefore keeps working for every config that leaves them off.

The Postgres and MySQL engines show what enforcing `read_only` takes. Their dialect
has a `ReadOnly` field, which `Dialect()` sets from the config, and `FormatQuery`
refuses an `INSERT`, `UPDATE`, `DELETE` or upsert before it renders anything, with
an error that wraps `core.ErrReadOnly` and names the statement. So every builder a
session or transaction hands out refuses a write where it is built. The datasource
installs `guard.InstallReadOnly` (`db/sql/gorm/guard`) on the handle `gorm.Open`
returned, before deriving any other from it, which refuses a write that reaches
gorm any other way, in its callbacks and again as each statement is sent. Their
executors mark the SQL a builder rendered, so the guard checks it for its shape and
not for words, and a column named `copy` does not make a builder read unrunnable;
the mark is internal to `db/sql/gorm`, so an engine outside it gets the word-by-word
check on its builder SQL too. The guard's and the dialect's refusals wrap
`core.ErrReadOnly`.

Postgres also asks the server for read-only transactions, with
`default_transaction_read_only=on` in the DSN, unless `options` set that parameter
themselves, in any spelling Postgres reads as it, or set `target_session_attrs` to
`read-write` or `read-only`. An empty `default_transaction_read_only` under
`options` sends nothing, and is what a datasource behind PgBouncer before 1.26 needs
unless PgBouncer tracks the parameter (`track_extra_parameters`, 1.20 and later on
Postgres 14 and later) or ignores it (`ignore_startup_parameters`): before 1.26 it
refuses any startup parameter it does not track, in every pool mode. The server's
refusal of a write is the driver's error, SQLSTATE 25006, not `core.ErrReadOnly`.
The guard refuses the `SET` and `set_config` calls that would switch the setting
off, but a `set_config` it does not see, sent on `DB()` or run by a function, switches
it off on that pooled connection for as long as the connection lives. These are
safety nets: a principal that can only read is the guarantee.

### 7. Test with pure string assertions

Dialect tests need no server. Write one case per method pinning either the exact
SQL or the exact refusal:

```go
func TestFormatReturningIsRefused(t *testing.T) {
    sql, _, err := (&SQLiteDialect{}).FormatReturning([]string{"id"})

    require.Error(t, err)
    require.True(t, dbCore.IsUnsupported(err))

    var e *dbCore.UnsupportedError
    require.ErrorAs(t, err, &e)
    assert.Equal(t, "RETURNING", e.Construct)
    assert.Empty(t, sql, "no partially rendered SQL may escape a refusal")
}
```

The shipped suites are the model: `db/sql/gorm/mysql/v2/dialect_test.go` (exact
SQL per method), `dialect_errors_test.go` (a refusal reaching the caller from
every clause position), and `db/sql/builder/builder_coverage_test.go` (every
`IQueryBuilder` method against all three dialects at once, which is where the
divergences show up side by side).

The cross-dialect file lives in the external test package `builder_test`, which is
what lets it import the concrete dialects: `v2` imports `builder`, and `builder`
never imports the test package, so there is no cycle.

Aim for ≥90% statement coverage, counted over the statements of the engine's
dialect files: `dialect.go`, and on SQL Server also `dialect_dml.go`,
`identifier.go` and `conditions.go`. Every shipped dialect is above it, and so is
`db/sql/builder`; `go test -coverprofile` on the package shows the figure per file.

String assertions prove what a dialect renders, not that the server accepts it.
The live suite in `e2e/tests` runs each engine's SQL against a real server: see
`TestMySQLQueriesRoundTripThroughTheDialect` and
`TestSQLServerQueriesRoundTripThroughTheDialect`, and [TESTING.md](TESTING.md) for
how to start the engines.

---

## What MySQL refuses, and why

Target: **MySQL 8.0+**, default charset `utf8mb4`, collation
`utf8mb4_unicode_ci`, and `sql_mode` including `ONLY_FULL_GROUP_BY` (MySQL 8's
default). The dialect never emits SQL that relies on loose grouping.

### Refused outright

| Construct | Why | Portable alternative |
|---|---|---|
| `RETURNING` | MySQL has no `RETURNING` on any statement. | After `INSERT`, read the generated key with `LAST_INSERT_ID()`. For `UPDATE`/`DELETE`, `SELECT` the affected rows inside the same transaction. |
| `DISTINCT ON` | Postgres-only. Degrading to plain `DISTINCT` would return a **different row set**, so it must not be substituted silently. | `ROW_NUMBER() OVER (PARTITION BY … ORDER BY …)` in a subquery, filtered to `rn = 1`. |
| `FULL OUTER JOIN` | No such join type in MySQL. The workaround changes the query's shape enough that the dialect must not apply it for you. | `LEFT JOIN … UNION … RIGHT JOIN …`. |
| `CUBE` | No MySQL equivalent. | Enumerate the grouping combinations as a `UNION ALL` of `GROUP BY` queries. |
| `GROUPING SETS` | No MySQL equivalent. | As `CUBE`. |
| `search_path` | A MySQL schema *is* a database, so there is no schema search path to set. Ignoring the setting would silently connect to the wrong place. | Point `db` at the schema you want. |
| `instance` | A named instance is a SQL Server concept. A config that names one was written for another engine, and connecting to whatever answers on the host's port would hide that. Postgres refuses it too. | Remove the key, and point `host` and `port` at the server. |
| `auth`, other than `method: sql` alone | MySQL signs in with the top-level `username` and `password` only. Any other method, or any other key under `auth`, would be dropped, and the sign-in would not be the one configured. Postgres refuses it too. | Remove the block, or leave only `method: sql` in it. |

### On a `read_only` datasource

With `read_only: true` the dialect refuses every write — `INSERT`, including
`INSERT … SELECT`, an `ON CONFLICT` upsert whether or not
`allow_unfaithful_upsert` is set, `UPDATE` and `DELETE` — before it renders
anything. The error wraps `core.ErrReadOnly` rather than being a
`core.UnsupportedError`, and it comes first: a write that MySQL could not express
anyway, such as one with `RETURNING`, is refused for being a write. Reads render,
and are refused, exactly as they are without the flag.

Unlike Postgres, the MySQL engine does not ask the server for read-only sessions.
The session variable is `transaction_read_only` on MySQL 8 and `tx_read_only` on
MySQL 5.7 before 5.7.20 and MariaDB before 11.1, and go-sql-driver sends a DSN
parameter it does not know as a `SET` that fails the connection when the server
does not know the name either. On a server you know, set the variable it has under
`options`, for example `transaction_read_only: "1"`; the server then refuses a
write with error 1792, which is the driver's error and does not wrap
`core.ErrReadOnly`. Either way, connect as a user that can only read.

### Translated

| Construct | Postgres | MySQL |
|---|---|---|
| Identifier quoting | `"tbl"."col"` | `` `tbl`.`col` `` — MySQL only accepts double quotes under `ANSI_QUOTES`, which is off by default and would break string literals. |
| `ROLLUP` | `GROUP BY ROLLUP (a, b)` | `GROUP BY a, b WITH ROLLUP` — a trailing modifier, not a prefix function. Because it modifies the whole grouping list it cannot be combined with a separate plain field list the way Postgres allows. |
| `ILIKE` | `ILIKE` | Rewritten to `LIKE`, which is case-insensitive under `utf8mb4_unicode_ci`. **On a `_bin` or `_cs` collation the comparison becomes case-sensitive** — that is a property of the column's collation, not of the rewrite. |
| `ON CONFLICT (cols) DO UPDATE` | `ON CONFLICT (cols) DO UPDATE SET …` | **Refused by default.** Opt in with `databases.<name>.allow_unfaithful_upsert: true` — see the warning below. |
| `ON CONFLICT DO NOTHING` | `ON CONFLICT DO NOTHING` | `ON DUPLICATE KEY UPDATE \`col\` = \`col\`` — the idiomatic MySQL no-op, self-assigning the first insert column. |
| Bare `OFFSET` | `OFFSET 20` is legal on its own | MySQL rejects `OFFSET` without `LIMIT`, so `LIMIT 18446744073709551615` is synthesised — the sentinel MySQL's own documentation prescribes. |
| Booleans | native `boolean` | `TINYINT(1)`; GORM handles the mapping. |

### Supported as-is

| Construct | Requirement |
|---|---|
| CTEs (`WITH`) | MySQL 8.0+ |
| Window functions | MySQL 8.0+ |
| `LATERAL` | MySQL 8.0.14+ |
| `?` placeholders | No change needed. The builder and conditions already emit `?`, not Postgres' `$n`, so placeholder style needed no work — which is why the MySQL port was as small as it was. SQL Server's go-mssqldb wants `@p1`…`@pN` instead. Its dialect still emits `?`, and gorm rewrites them when the statement runs, which is why the SQL Server executor sends every statement through gorm (see the SQL Server tables below). |

### `ON CONFLICT … DO UPDATE` is refused by default

The dialect's governing rule is that a construct MySQL cannot express returns an
error rather than emitting SQL the server will reject. This translation was
originally the one exception: it emitted *valid but wrong* SQL and documented why.
That is the worse failure mode — the server accepts it, so a doc warning does not
stop it executing — so it now errors:

```
mysql does not support ON CONFLICT ... DO UPDATE; MySQL's ON DUPLICATE KEY UPDATE
fires on any unique index rather than the conflict target you named, so the
translation is not faithful; set databases.<name>.allow_unfaithful_upsert: true (or
MySQLDialect.AllowUnfaithfulUpsert, when you build the dialect yourself) if the table
has exactly one unique constraint, or do the read-then-write explicitly in a
transaction
```

A caller who has read the rest of this section and knows their table has exactly one
unique constraint can opt in **per datasource**, in the config:

```yaml
databases:
  main:
    driver: mysql_gorm
    host: localhost
    db: app
    allow_unfaithful_upsert: true
```

That is the setting an app using the ORM wants, and until v2.0.0 it did not exist. The
flag lived only on the dialect struct, while `Dialect()` built the dialect itself and
`session.Query()` / `Transaction()` build every builder from it — so the documented opt-in
could be taken only by constructing a builder by hand and bypassing the ORM:

```go
// Still available, for a builder you construct yourself.
dialect := &mysql.MySQLDialect{AllowUnfaithfulUpsert: true}
b := builder.New(dialect)
```

The key is MySQL-only. Postgres expresses `ON CONFLICT (cols) DO UPDATE` exactly, so a
Postgres datasource ignores it — the same way it is the Postgres-only
`prefer_simple_protocol` that MySQL ignores.

`ON CONFLICT DO NOTHING` is unaffected. Its self-assignment translation
(``ON DUPLICATE KEY UPDATE `col` = `col` ``) is faithful, so it needs no opt-in —
which is what lets the ORM's many-to-many path work on every engine unchanged.

### Why the translation is not faithful

The semantics genuinely differ and no amount of rewriting fixes it:

- Postgres' `ON CONFLICT (a) DO UPDATE` fires **only** on a conflict in the named
  columns. MySQL's `ON DUPLICATE KEY UPDATE` fires on a duplicate in **any**
  `UNIQUE` index or the `PRIMARY KEY`. The conflict-target column list therefore
  has nowhere to go and is dropped.
- On a table with **more than one** unique index this is worse than imprecise.
  MySQL's own documentation says an `INSERT ... ON DUPLICATE KEY UPDATE` against
  such a table behaves like `UPDATE … WHERE a=1 OR b=2 LIMIT 1`, and advises: "In
  general, you should try to avoid using an `ON DUPLICATE KEY UPDATE` clause on
  tables with multiple unique indexes." Which row gets updated is not something you
  control.

If you are writing an upsert that must be portable, either keep the target table
to a single unique constraint, or write the read-then-write explicitly inside a
transaction rather than relying on the translation.

### One thing MySQL 8 *does* accept

`DROP TABLE … CASCADE` is **not** a MySQL syntax error, contrary to a common
assumption. MySQL 8 documents `RESTRICT` and `CASCADE` on `DROP TABLE` as accepted
no-ops, "permitted to make porting easier". Going through GORM's `Migrator` still
gets you the right thing per engine — the Postgres driver emits `CASCADE`, where
it actually does something, and the MySQL driver does not.

---

## What SQL Server refuses, and translates

Target: **SQL Server 2016 or newer, and Azure SQL Database**, through the
`sqlserver_gorm` driver. It is not part of `driver/builtin`, which would link
go-mssqldb into every Postgres and MySQL app; import it next to your other driver
import, in `pkg/provider/bootstrap.go`:

```go
import _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
```

Connecting and signing in, Microsoft Entra ID included, are in [SQLSERVER.md](SQLSERVER.md).

The governing rule is MySQL's: what T-SQL cannot express is refused with a
`dbCore.UnsupportedError`, and nothing is translated into SQL that runs but means
something else. SQL Server adds a second concern. The schemas it is pointed at are
often EF Core's, with names such as `[dbo].[2024Orders]` and `[Order]` that are valid
only bracketed, so the dialect brackets every identifier it emits: in the clauses it
renders itself, and in conditions through the rendering seam (see
[Conditions and the rendering seam](#conditions-and-the-rendering-seam)).

Each row names the test that pins it. Unit tests are in `db/sql/gorm/sqlserver/v2`;
the live ones, marked *live*, run against SQL Server 2022 in
`e2e/tests/live_sqlserver_test.go`.

### Refused in the SQL

| Construct | Why | Instead | Pinned by |
|---|---|---|---|
| `DISTINCT ON`, from `DistinctOn()` or written into the first select item | T-SQL has none, and a plain `DISTINCT` returns a different row set. | `ROW_NUMBER() OVER (PARTITION BY … ORDER BY …)` in a subquery, filtered to 1. | `TestFormatSelectRefusesDistinctOn`, `TestDistinctWrittenIntoTheFirstItemIsLifted`; *live* `TestSQLServerQueriesRoundTripThroughTheDialect/refusals` |
| `NATURAL JOIN`, an unknown join type, `CROSS JOIN` with an `ON` | T-SQL has no join that matches columns by name, and a `CROSS JOIN` takes no `ON`. | Spell the condition with `ON`, or use an `INNER JOIN`. | `TestFormatJoinRefusesNaturalAndUnknown`, `TestCrossJoinWithConditionIsRefused` |
| `LATERAL` with a real `ON` condition; a right or full `LATERAL` | `LATERAL` becomes `APPLY`, which takes no `ON` and has no right or full form. | Move the condition into the subquery's `WHERE`, where it can refer to the outer row. | `TestLateralWithRealOnIsRefused`, `TestLeftLateralBecomesOuterApply` (right and full) |
| `ORDER BY` in a subquery, derived table or CTE without `Limit` or `Offset` | Msg 1033. | Drop the `ORDER BY`, or add a `Limit`. | `TestOrderByInSubqueryWithoutLimitIsRefused`, `TestScopeRulesTable`; *live* `…/refusals`, which also shows the server's Msg 1033 and its hint |
| `ORDER BY`, `Limit` or `Offset` on a query with `UNION` or on a `UNION` arm; a `UNION` nested in an arm | The builder cannot tell whether they were meant for the first arm or for the whole `UNION`, and T-SQL orders and pages only the whole. | Wrap the `UNION` in `Subquery(union, alias)` and order or page that. | `TestUnionWithOrderByOrLimitIsRefused`, `TestUnionArmMustBeBare` |
| `WITH` inside a CTE, a subquery or a `UNION` arm | T-SQL allows `WITH` only at the start of a statement. An `INSERT … SELECT`'s CTEs are hoisted in front of the `INSERT` instead. | Move the CTE to the outermost query. | `TestNestedCTEIsRefused`, `TestInsertSelectHoistsCTEs` |
| A write as a CTE body, a subquery or a `UNION` arm | T-SQL nests only `SELECT`s there. | Run the write as its own statement. | `TestNestedWriteIsRefusedWhenNotReadOnly` |
| `DISTINCT` with an `Offset` and no `ORDER BY` | `OFFSET` needs an `ORDER BY`, and the `ORDER BY (SELECT NULL)` the dialect would supply is Msg 145 under `DISTINCT`. | Order by the selected columns. | `TestDistinctOffsetWithoutOrderByIsRefused`; *live* `TestSQLServerDistinctWithSelectNullOrderBy` |
| `OrderBy()` on something that is not a column | SQL Server refuses a constant sort key (Msg 1008), which is what binding it would make. | Pass a trusted expression to `OrderByRaw`. | `TestFormatOrderByRefusesNonIdentifier` |
| In a window defined with `Window()`, a `PARTITION BY` or `ORDER BY` item that is not a column; a `GROUPS` frame; `EXCLUDE`; a `RANGE` frame with an offset | T-SQL has `ROWS` and `RANGE` frames only, no exclusion, and `RANGE` takes only `UNBOUNDED` and `CURRENT ROW`. The query's windows are checked whether or not `Over()` places them, and so is a definition passed to `FormatWindow`. | Partition by columns; use `ROWS` for an offset. | `TestBuilderWindowsRenderThroughTheDialect`, `TestFormatWindowRefusesWhatTSQLCannotSay` |
| An aggregate `FILTER (WHERE …)` | T-SQL has no `FILTER`. | `SUM(CASE WHEN … THEN x END)`, `COUNT(CASE WHEN … THEN 1 END)`. | `TestAggregateFilterIsRefused` |
| The operators `~`, `~*`, `!~`, `!~*`, `SIMILAR TO`, `REGEXP`, `RLIKE`, `@>`, `<@`, `&&`, `?`, `?\|`, `?&`, `->`, `->>`, `#>`, `#>>`, `@@`, `<=>`, and any operator it does not know | They are Postgres's or MySQL's. An operator is SQL placed between its operands, so one the dialect does not recognise is never emitted. | `LIKE` with `[a-z]` classes, `JSON_VALUE`/`OPENJSON`, `CONTAINS`/`FREETEXT`, `IS NOT DISTINCT FROM` (SQL Server 2022, Azure SQL); each refusal names the one that applies. | `TestPgAndMySQLOnlyOperatorsAreRefused`; *live* `…/refusals` |
| A `LIKE … ESCAPE` that is not exactly one character other than `'`, `?` and `@` | T-SQL takes one escape character. gorm reads a `?` anywhere in the statement as a placeholder, so `ESCAPE '?'` took the next argument into the literal and moved every later one along, and an `@` switches it to named parameters. | Use one other character, such as `!`. | `TestLikeEscapeMustBeOneCharacter` |
| `RETURNING` on a `SELECT`; an expression in `RETURNING` | `OUTPUT`, which `RETURNING` becomes, belongs to `INSERT`, `UPDATE`, `DELETE` and `MERGE`, and lists columns. | Return columns from the write. | `TestReturningOnSelectIsRefused`, `TestOutputExpressionIsRefused` |
| An `UPDATE` or `DELETE` with `ORDER BY`, `Offset`, joins, `FROM`, `GROUP BY`, `HAVING` or `UNION` | `UPDATE TOP (n)` and `DELETE TOP (n)` take no `ORDER BY`, and the rest would be dropped. | A CTE with `TOP … ORDER BY`, updated or deleted through. | `TestUpdateWithOrderByIsRefused`, `TestUpdateWithJoinIsRefused`, `TestDeleteTop`, `TestDeleteWithClausesItDoesNotTakeIsRefused` |
| An `INSERT … VALUES` of more than 1000 rows; clauses an `INSERT` would ignore | Msg 10738. An upsert's `VALUES` is a derived table, which has no row limit, so only the parameter cap below bounds it. | Split the `INSERT`. | `TestInsertMoreThan1000RowsIsRefused`, `TestUpsertOfMoreThan1000RowsIsNotRefused`, `TestInsertIgnoredClausesAreRefused` |
| `ON CONFLICT` without conflict columns, or with a column that is not inserted | The translation matches on them. | Name the key columns, and insert them. | `TestOnConflictWithoutColumnsIsRefused`, `TestOnConflictColumnNotInsertedIsRefused` |
| More than 2098 bound parameters, as the dialect renders them or as gorm expands an `IN (?)` slice on a raw path | SQL Server takes 2100 per request, and go-mssqldb's `sp_executesql` uses two (Msg 8003 at the server). SQL that gorm builds itself, through its chain API or `Create`, `Update` and `Delete`, is not counted and reaches the server. | Chunk `IN` lists and batch inserts; `dbCore.BindParameterLimit` reports the cap. | `TestTooManyParametersIsRefused`, `TestParamLimitCountsExpandedSliceArgs`; *live* `TestSQLServerParameterLimit`, which also shows the server refusing the 2099th |
| A bracketed name containing `@` or `?`, wherever a name stands: a column, alias or CTE name, a table in `FROM`, a join or a write, a select item, a `GROUP BY` item | gorm would read either as a parameter. An expression the dialect emits as the caller wrote it, such as `COUNT(x)` or a `RawCondition`, is the caller's SQL, and gorm reads any `?` in it, quoted or not. | Rename the object, or reach it through a view or synonym. | `TestBracketedPartWithAtOrQuestionMarkIsRefused`, `TestBracketedMarkerIsRefusedWhereTextPassesThrough` |
| A `GROUP BY` position, `GroupBy("1")` | Postgres and MySQL read it as the first select item; T-SQL has no `GROUP BY` position, reads the constant 1, and refuses it (Msg 164). Nor does it take a select-list alias there, which the server refuses (Msg 207). | Group by the column or expression itself. | `TestGroupByPositionIsRefused`; *live* `TestSQLServerCountByQueryShapes` |

### Refused in the datasource config

| Key | Why | Pinned by |
|---|---|---|
| `search_path` | SQL Server has no per-connection schema search path to set. Qualify other schemas in `TableName()`, such as `"sales.Orders"`. | `TestSearchPathIsRefused`; *live* `…/refusals` |
| An option a typed key owns (`database`, `user id`, `password`, `server`, `port`, `encrypt`, `fedauth`, …), `change password`, `columnencryption`, and any unknown option | `options` is a closed list with snake_case aliases for go-mssqldb's spaced keys, and a typed key has one place to be set. The error names the key to use, or the nearest known option. | `TestOptionsOwnedByTypedConfigAreRefused`, `TestUnsupportedOptionsAreRefused`, `TestUnknownOptionIsRefusedWithASuggestion` |
| On an Azure SQL host, `ssl: false` or `disable`, and `trust_server_certificate`; anywhere, `trust_server_certificate` with `ssl: strict` | Azure SQL refuses an unencrypted connection, and its certificate always verifies, so trusting any certificate only removes the check. `strict` verifies the certificate by definition. | `TestAzureHostRefusesDisabledEncryption`, `TestAzureHostRefusesTrustServerCertificate`, `TestTrustServerCertificateWithStrictIsRefused` |
| `options.application_intent: ReadOnly` without `read_only: true` | It routes the connection to a read-only node, where every write the datasource still allows would fail. `read_only: true` sets the intent itself. | `TestApplicationIntentReadOnlyWithoutReadOnlyIsRefused` |

### Translated

| Construct | Postgres | SQL Server | Pinned by |
|---|---|---|---|
| Identifier quoting | `"tbl"."col"` | `[tbl].[col]`, with `]` doubled, idempotent. A digit-leading segment such as `2024Orders` and a reserved word such as `Order` are identifiers; `1`, `1e5` and `0x1F` are literals. Applied in conditions, `JOIN … ON` and the `WHERE` of an `UPDATE` or `DELETE` too, and to the column after a `RawCondition`'s `?.` in any script: `?.Größe` is `[m].[Größe]`. | `TestQuoteIdentifierBrackets`, `TestIsIdentifierAcceptsEFNames`, `TestFormatWhereQuotesConditionIdentifiers`, `TestJoinOnIsValidated`, `TestPlaceholderColumnMayBeAnyLetters`; *live* `TestSQLServerHyphenatedDatabaseName`, `…/[Order]` |
| Placeholders | `?`, rewritten to `$n` | `?` in the SQL, rewritten by gorm to `@p1`…`@pN` when it runs. The dialect never emits `@`, which would switch gorm to named parameters. | `TestPlaceholderStyleIsQuestionMark`, `TestNoAtSignInDialectOutput`, `TestExecRewritesQuestionMarksToAtP` |
| `Limit(n)`, alone or with `Offset(0)` | `LIMIT n` | `SELECT TOP (n)`, which needs no `ORDER BY` | `TestLimitWithoutOffsetIsTop`, `TestOffsetZeroWithLimitIsTop`; *live* `…/TOP` |
| `Limit(0)` | `LIMIT 0` | `TOP (0)`; `FETCH NEXT 0` is invalid | `TestTopZeroIsLegal`, `TestLimitZeroWithOffsetIsTopZero`; *live* `…/TOP` |
| `Offset(m)` | `OFFSET m` | `ORDER BY … OFFSET m ROWS [FETCH NEXT n ROWS ONLY]`, with `ORDER BY (SELECT NULL)` when the query has none | `TestOffsetSynthesisesOrderBySelectNull`, `TestOffsetFetchWithOrderBy`, `TestBareOffsetIsOffsetRows`; *live* `…/OFFSET_FETCH` |
| `DISTINCT` written into the first select item | as written | Lifted into its own place, before `TOP`: `SELECT DISTINCT TOP (n) …`, not `TOP (n) DISTINCT`, which is Msg 156 | `TestDistinctWrittenIntoTheFirstItemIsLifted`; *live* `TestSQLServerDistinctWithSelectNullOrderBy` |
| `Limit` on an `UPDATE` or `DELETE` | — | `UPDATE TOP (n)`, `DELETE TOP (n) FROM` | `TestUpdateTop`, `TestDeleteTop`; *live* `…/DELETE_TOP_(1)` |
| `RETURNING` | `RETURNING …` | `OUTPUT INSERTED.[c]`, or `OUTPUT DELETED.[c]` on a `DELETE`, placed where T-SQL wants it. The server refuses it on a table with an enabled trigger on the statement's own action (Msg 334): a trigger on `UPDATE` does not block an `INSERT`'s. `ExecInsert` reads the key there instead (see below). | `TestFormatReturningIsOutputInserted`, `TestInsertOutputPlacement`, `TestDeleteOutputDeleted`; *live* `…/OUTPUT_on_each_statement` |
| `ON CONFLICT (cols) DO NOTHING` | as written | `INSERT … SELECT … FROM (VALUES …) AS [src] WHERE NOT EXISTS (… WITH (UPDLOCK, HOLDLOCK) …)`. The hints hold concurrent inserts of one key apart, also where readers see a snapshot: one inserts it, the others then find it. Two things differ from Postgres. Rows of one statement that share a key are not deduplicated: the second fails with Msg 2627, so deduplicate the rows first. And the probe takes update locks on the keys that exist as well as on the missing ones, in whatever order the plan reads them, so concurrent multi-row statements whose keys overlap can deadlock (Msg 1205, rolled back whole and safe to retry, with a hint saying so); sort each statement's keys, keep batches small or send overlapping ones from one worker, and retry on 1205. MERGE's insert-only form was measured and not used: it avoids the deadlock where the keys exist, and deadlocks more where both statements insert new keys through a nonclustered unique index, an EF table's usual shape. | `TestDoNothingIsInsertWhereNotExists`, `TestServerErrorHints`; *live* `…/DO_NOTHING_twice`, `…/concurrent_upserts_of_one_key`, `…/concurrent_overlapping_batches` |
| `ON CONFLICT (cols) DO UPDATE` | as written | `MERGE … WITH (HOLDLOCK) … ON` the conflict columns. It matches on the columns you name, so, unlike MySQL's, it is faithful and needs no opt-in. | `TestDoUpdateIsMerge`; *live* `…/MERGE_twice`, `…/concurrent_upserts_of_one_key` |
| `LATERAL` | `LATERAL` | `CROSS APPLY`; a left one `OUTER APPLY`. A trivial `ON` (none, `true`, `1=1`) is dropped. | `TestLateralBecomesCrossApply`, `TestLeftLateralBecomesOuterApply`; *live* `…/CROSS_APPLY_and_OUTER_APPLY` |
| `ROLLUP`, `CUBE`, `GROUPING SETS` | ISO form | ISO form, as Postgres | `TestFormatGroupByISOModifiers`; *live* `…/ROLLUP,_CUBE_and_GROUPING_SETS` |
| `ILIKE` | `ILIKE` | `LIKE`, as an operator and inside a `RawCondition`, where only the operator is rewritten, never the word inside a string literal or a delimited name. Case-insensitive under the default collations, EF Core's included; **case-sensitive on a `_CS_` or binary collation.** | `TestFormatWhereRewritesILIKE`, `TestILIKEInsideALiteralOrANameIsLeftAlone`; *live* `…/ILIKE` |
| `LIKE` patterns | `%` and `_` are the wildcards | `%` and `_`, and also `[…]`, a character class, and `[^…]`: the pattern is bound as it is, so `ref [ab]%` matches `ref b` here and only a literal `ref [ab]…` on Postgres and MySQL. A portable app that escapes `%` and `_` in user input gets wider matches here, so escape `[` in a pattern from user input too, as `[[]`, or with `LikeEscape(…, "!")` and `![`. | *live* `…/LIKE_character_class` |
| A window placed with `Over(name)` | core's text | Rendered by the dialect in place of core's text: bracketed, `n PRECEDING` for an offset, and refused where T-SQL has no form (see above). The name before `OVER`, the function, stays as written. | `TestBuilderWindowsRenderThroughTheDialect`; *live* `…/window_through_Over` |
| An empty `IN` list | `f IN ()` | `1=0`, and `1=1` for `NOT IN`: T-SQL has no boolean literals | `TestEmptyInRendersFalsePredicate` |

### Executed differently

Some differences are in what the executor sends, not in what the dialect renders.

| Behaviour | Why | Pinned by |
|---|---|---|
| A builder write reports its own row count, read from `ROWCOUNT_BIG()` in the same batch | go-mssqldb reports every row the request touched, a trigger's included, so a trigger without `SET NOCOUNT ON` inflated the count, and an `UPDATE` guarded by a version column that matched nothing looked like a success. | `TestExecTakesRowsAffectedFromRowcountBig`; *live* `TestSQLServerRowsAffectedIgnoresTriggerRows`, `TestSQLServerRowsAffectedIsMatchedRows` |
| `ExecRaw` reports the driver's count, trigger rows included | It runs the caller's SQL as written. Append `; SELECT ROWCOUNT_BIG()` and read it when the count must be the statement's own. | *live* `TestSQLServerExecRawCountIncludesTriggerRows` |
| `ExecInsert` reads the key with `SCOPE_IDENTITY()` in the same batch | go-mssqldb has no `LastInsertId`, and `OUTPUT` is refused on a table with a trigger on `INSERT`. `SCOPE_IDENTITY()` is the `INSERT`'s own key, never a trigger's, and only an `IDENTITY` key: a key from a default or a sequence, or under an `INSTEAD OF INSERT` trigger, is not reported. The ORM's `Create` inserts this way for a model that implements `orm.TableWithTriggers`, whose `Update` then re-reads `grgorm:"readback"` fields with a `SELECT` instead of `OUTPUT`. | `TestExecInsertReportsScopeIdentity`, `TestEFShapes`; *live* `TestSQLServerRowcountBigAfterInsert`, `TestSQLServerTriggerTableCreate` |
| Every connection runs `SET XACT_ABORT ON` | Without it an error fails one statement and keeps the transaction, so code that swallowed the error committed the rest. With it the error dooms the transaction, as on Postgres. | *live* `TestSQLServerXactAbortDoomsASwallowedError`, `TestMigrateUpRecordsOnlyWhatCommitsOnSQLServer` |
| A `[]byte` is bound whole, and a nil `*[]byte` or `[]byte` as a `varbinary` NULL | gorm expands a slice bound right after `(`, so the first `VALUES` value arrived one parameter per byte; and go-mssqldb sends an untyped NULL as `nvarchar`, which a `varbinary` column refuses (Msg 257). | `TestByteSliceArgsAreNotExpanded`, `TestNilBytePointerBindsTypedNull`; *live* `TestSQLServerByteSliceFirstValue`, `TestSQLServerNullVarbinaryRoundTrip` |
| A `json.RawMessage`, or any `[]byte` type that marshals itself as JSON, is bound as text | Sent as `varbinary` to an `nvarchar` column, EF Core's JSON column type, the server converts it without an error by reading the bytes as UTF-16, and stores mojibake. Any other `[]byte` is `varbinary`: convert it to a `string` for a text column. | `TestJSONBytesBindAsText`; *live* `TestSQLServerArgumentsKeepTheirMeaning/json` |
| A `time.Time`, `*time.Time` or `sql.NullTime` is bound in UTC | go-mssqldb sends a time as a `datetimeoffset` with its own offset, and SQL Server keeps the wall clock and drops the offset converting it to `datetime2` or `datetime`, EF Core's types for a `DateTime`: 10:00 in Zurich was stored as 10:00 and read back as 10:00 UTC, and an equality lookup with it matched nothing. In UTC the instant survives every type, as it does on Postgres and MySQL. A `datetimeoffset` column then holds `+00:00`; bind `mssql.DateTimeOffset(t)` where the offset itself must be kept. An app writing through gorm's own API (`GetDriver`) should send UTC. | `TestTimesBindInUTC`; *live* `TestSQLServerArgumentsKeepTheirMeaning/time` |
| A Go `string` is sent as `nvarchar` | go-mssqldb's choice. Compared with a `varchar` column, common in schemas not written by EF Core and in `IsUnicode(false)` columns, the column is converted (`CONVERT_IMPLICIT`) under a `SQL_` collation, SQL Server's default and Azure SQL's, so the lookup scans the index instead of seeking it. Under an upsert's locking hints that scan locks the whole index, serialising every upsert on the table (32 workers' single-row `DO NOTHING`s on a 20,000-row table took 13 s, against 0.2 s bound as `varchar`), and `DO NOTHING` can then deadlock one row at a time. Bind a `varchar` key as `mssql.VarChar(s)`, or `mssql.VarCharMax(s)`, which the executor passes through. | `TestBindArgs` |
| `uniqueidentifier` reads and writes in the order other clients show | The datasource sets go-mssqldb's `guid conversion`; use `uuid.UUID` and `uuid.NullUUID`. go-mssqldb's own `mssql.UniqueIdentifier` and `NullUniqueIdentifier` reorder the bytes themselves, so with the conversion on they read every GUID with its first three groups reversed and bind one that matches nothing, silently; set `options.guid_conversion: false` for code built on them. | *live* `TestSQLServerGuidConversionRoundTrip` |
| `read_only` and `external_schema` are enforced on the connection | The dialect refuses a write where it is built; gorm callbacks refuse writes, or DDL including `sp_rename` and `sp_updateextendedproperty`, before they are sent. | `TestReadOnlyRefusesEveryWrite`, `TestExternalSchemaGuardRefusesSQLServerMigratorDDL`; *live* `TestSQLServerReadOnlyRefusesWritesThroughEveryPath`, `TestSQLServerExternalSchemaRefusesAutoMigrateCommentChange` |
| Server errors an app on this engine commonly meets carry a hint | Msg 334, 544, 271–273, 257, 8003, 10738, 1205, 1033, 8127, 8155, 8156, 3906, 18456, 4060 and 4063 name a symptom; the hint names the model or config change, or for 1205, a deadlock, that the statement is safe to retry. | `TestServerErrorHints`; *live* `TestSQLServerConnectsWithSQLLogin`, `TestSQLServerNullVarbinaryRoundTrip` |

---

## Conditions and the rendering seam

`dbCore.Condition.ToSQL()` takes no dialect, and still does not:

```go
type Condition interface {
    ToSQL() (string, []interface{})
}
```

Conditions render themselves. Up to and including v2.4.3 that was a boundary: every
identifier a condition emitted was verbatim, and a subquery nested *inside* a
condition — `InSubquery`, `Exists` and friends — went through
`db/sql/core/sql_conditions.go`'s package-level `buildSubquerySQL`, which is not
dialect-aware, spells `LIMIT`/`OFFSET` the Postgres and MySQL way, and emits
`DISTINCT ON` if it finds it. Postgres and MySQL mostly get away with that. An
engine whose identifiers need quoting where theirs do not — a digit-leading table,
a reserved word — cannot, because nothing it controls ever sees them.

Giving `Condition.ToSQL` a dialect parameter would close it, and would break every
condition type an app has written. The seam is additive instead
(`db/sql/core/render.go`):

- **`dbCore.RenderContext`** is how an engine takes part. Every hook is optional,
  and a nil one keeps today's behaviour for its slot: `QuoteIdentifier`,
  `IsIdentifier`, `Subquery` (renders a nested `*Query` through the dialect),
  `Operator` (maps or refuses an operator), `EmptyIn`, `Raw` (rewrites a
  `RawCondition`'s SQL) and `Strict`, which turns every silent degradation into an
  error, such as a non-identifier in an identifier slot, which would otherwise be
  bound as a value and compared as text.
- **`dbCore.ContextRenderer`**, `ToSQLContext(ctx)`, is implemented by every built-in
  condition, and `ToSQL()` is `ToSQLContext(nil)`: with a nil context the output is
  byte for byte what it always was, which `db/sql/core/testdata/conditions.golden`
  pins.
- **`RenderCondition`, `RenderWhere` and `RenderHaving`** render under a context. An
  app-defined condition that does not implement `ContextRenderer` still renders,
  through its `ToSQL`, and under a `Strict` context is validated first
  (`dbCore.ValidateCondition`). A struct that *embeds* a built-in condition is
  rendered that way too, so a filter its own `ToSQL` adds is never dropped.

The Postgres and MySQL dialects do not use the seam, so their output is unchanged,
and the old boundary still applies to them: a `DistinctOn()` subquery placed inside a
condition renders Postgres's `DISTINCT ON` on MySQL. Hoist it into the outer query,
where the dialect sees it.

The SQL Server dialect renders every `WHERE`, `HAVING`, `JOIN … ON` and the `WHERE`
of an `UPDATE` or `DELETE` under its own strict context
(`db/sql/gorm/sqlserver/v2/conditions.go`): identifiers bracketed, nested
subqueries rendered by its own `formatSelect`, `ILIKE` as `LIKE`, the operators of
other engines refused and an empty `IN` as `1=0`. A new dialect whose identifiers
need more than the Postgres/MySQL rule should do the same. An app condition with
identifiers or subqueries of its own implements `ContextRenderer` to have them
quoted there.

---

## Reference

- `db/sql/core/interfaces.go` — `SQLDialect`, `IQueryBuilder`
- `db/sql/core/errors.go` — `UnsupportedError`, `Unsupported`, `IsUnsupported`
- `db/sql/core/sql_types.go` — `Query` and every clause type
- `db/sql/core/render.go` — `RenderContext`, `ContextRenderer`, `RenderCondition`, `RenderWhere`, `RenderHaving`
- `db/sql/core/capabilities.go` — `BindParameterLimiter`, `TriggerSensitiveReturning`
- `db/sql/core/policy.go`, `db/sql/core/readonly.go` — `DataSourcePolicy`, `ErrReadOnly`, `ErrExternalSchema`, the SQL guards
- `db/sql/builder/builder.go` — the engine-agnostic builder
- `db/sql/gorm/guard/guard.go` — `InstallReadOnly`, `InstallExternalSchema`
- `db/sql/gorm/postgres/v2/dialect.go` — the reference implementation
- `db/sql/gorm/mysql/v2/dialect.go` — the reference for refusing constructs
- `db/sql/gorm/sqlserver/v2/dialect.go`, `dialect_dml.go`, `identifier.go`, `conditions.go` — the reference for quoting every identifier and for using the rendering seam
- `db/sql/driver/sqlserver/sqlserver.go` — registering a driver outside `builtin`
- `e2e/tests/live_sqlserver_test.go` — the SQL Server live cases the tables above name
