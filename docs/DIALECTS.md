# SQL dialects

The query builder is engine-agnostic. It accumulates clauses into a
`dbCore.Query` and delegates every byte of SQL rendering to an injected
`dbCore.SQLDialect`. This document explains how to implement a dialect for a
third engine, and records exactly what MySQL refuses and why.

**Before v2 none of this was reachable.** `NewBuilder()` hard-coded
`&PostgresDialect{}`, and the `Config{Dialect}` struct that looked like the
intended seam was never constructed or read by anything. The only way to reach
another engine was to fork the whole 886-line builder.

---

## The seam

```
db/sql/config      typed datasource configuration, engine-independent
db/sql/core        interfaces (SQLDialect, IQueryBuilder, Query, conditions)
db/sql/builder     the engine-agnostic Builder — no SQL syntax of its own
db/sql/driver      driver-name -> constructor registry
db/sql/gorm/postgres/v2   PostgresDialect + Postgres datasource
db/sql/gorm/mysql/v2      MySQLDialect + MySQL datasource
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

## Implementing a third dialect

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
request-derived string could reach the SQL text. Both shipped dialects use the
same three-way rule, and a third should too:

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
`IQueryBuilder` method against both dialects at once, which is where the
divergences show up as a pair of assertions).

The cross-dialect file lives in the external test package `builder_test`, which is
what lets it import the concrete dialects: `v2` imports `builder`, and `builder`
never imports the test package, so there is no cycle.

Aim for ≥90% statement coverage. The shipped dialects are at 96.6% (Postgres) and
95.3% (MySQL), with `db/sql/builder` at 96.6%.

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

### Translated

| Construct | Postgres | MySQL |
|---|---|---|
| Identifier quoting | `"tbl"."col"` | `` `tbl`.`col` `` — MySQL only accepts double quotes under `ANSI_QUOTES`, which is off by default and would break string literals. |
| `ROLLUP` | `GROUP BY ROLLUP (a, b)` | `GROUP BY a, b WITH ROLLUP` — a trailing modifier, not a prefix function. Because it modifies the whole grouping list it cannot be combined with a separate plain field list the way Postgres allows. |
| `ILIKE` | `ILIKE` | Rewritten to `LIKE`, which is case-insensitive under `utf8mb4_unicode_ci`. **On a `_bin` or `_cs` collation the comparison becomes case-sensitive** — that is a property of the column's collation, not of the rewrite. |
| `ON CONFLICT (cols) DO UPDATE` | `ON CONFLICT (cols) DO UPDATE SET …` | **Refused by default.** Opt in with `MySQLDialect{AllowUnfaithfulUpsert: true}` — see the warning below. |
| `ON CONFLICT DO NOTHING` | `ON CONFLICT DO NOTHING` | `ON DUPLICATE KEY UPDATE \`col\` = \`col\`` — the idiomatic MySQL no-op, self-assigning the first insert column. |
| Bare `OFFSET` | `OFFSET 20` is legal on its own | MySQL rejects `OFFSET` without `LIMIT`, so `LIMIT 18446744073709551615` is synthesised — the sentinel MySQL's own documentation prescribes. |
| Booleans | native `boolean` | `TINYINT(1)`; GORM handles the mapping. |

### Supported as-is

| Construct | Requirement |
|---|---|
| CTEs (`WITH`) | MySQL 8.0+ |
| Window functions | MySQL 8.0+ |
| `LATERAL` | MySQL 8.0.14+ |
| `?` placeholders | No change needed. The builder and conditions already emit `?`, not Postgres' `$n`, so placeholder style needed no work — which is why the MySQL port was as small as it was. |

### `ON CONFLICT … DO UPDATE` is refused by default

The dialect's governing rule is that a construct MySQL cannot express returns an
error rather than emitting SQL the server will reject. This translation was
originally the one exception: it emitted *valid but wrong* SQL and documented why.
That is the worse failure mode — the server accepts it, so a doc warning does not
stop it executing — so it now errors:

```
mysql does not support ON CONFLICT ... DO UPDATE; MySQL's ON DUPLICATE KEY UPDATE
fires on any unique index rather than the conflict target you named, so the
translation is not faithful; set MySQLDialect.AllowUnfaithfulUpsert if the table has
exactly one unique constraint, or do the read-then-write explicitly in a transaction
```

A caller who has read the rest of this section and knows their table has exactly one
unique constraint can opt in:

```go
dialect := &mysql.MySQLDialect{AllowUnfaithfulUpsert: true}
b := builder.New(dialect)
// or, per datasource, by constructing the session's builder with it
```

`ON CONFLICT DO NOTHING` is unaffected. Its self-assignment translation
(``ON DUPLICATE KEY UPDATE `col` = `col` ``) is faithful, so it needs no opt-in —
which is what lets the ORM's many-to-many path work on both engines unchanged.

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

## Known boundary: conditions are dialect-independent

`dbCore.Condition.ToSQL()` takes no dialect:

```go
type Condition interface {
    ToSQL() (string, []interface{})
}
```

Conditions render themselves, and a subquery nested *inside* a condition —
`InSubquery`, `Exists`, and friends — goes through
`db/sql/core/sql_conditions.go`'s package-level `buildSubquerySQL`, which is not
dialect-aware and emits Postgres-flavoured `DISTINCT ON` if it finds it.

In practice this is reachable only by building a subquery with `DistinctOn()` and
placing it inside a condition rather than in the main query. The main-query path
is fully dialect-aware and refuses it correctly.

Closing this properly means giving `Condition.ToSQL` a dialect parameter, which
breaks every custom condition type an app has written. It was left alone for v2 as
disproportionate to the exposure. If you hit it, hoist the `DISTINCT ON` into the
outer query, where the dialect sees it.

---

## Reference

- `db/sql/core/interfaces.go` — `SQLDialect`, `IQueryBuilder`
- `db/sql/core/errors.go` — `UnsupportedError`, `Unsupported`, `IsUnsupported`
- `db/sql/core/sql_types.go` — `Query` and every clause type
- `db/sql/builder/builder.go` — the engine-agnostic builder
- `db/sql/gorm/postgres/v2/dialect.go` — the reference implementation
- `db/sql/gorm/mysql/v2/dialect.go` — the reference for refusing constructs
