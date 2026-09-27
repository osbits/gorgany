# Gorgany Framework - AGENTS.md

## Project Overview

Gorgany is a modular Go web framework for building scalable HTTP servers and CLI applications. It provides a complete, opinionated architecture with authentication, authorization, ORM, validation, events, and more.

**Go version:** the `go` directive in `go.mod`

## Build & Test Commands

```bash
# Run all tests
go test ./...

# Run tests for a specific package
go test ./auth/...
go test ./http/...
go test ./db/...
go test ./service/...
go test ./model/...

# Run with verbose output
go test -v ./...

# Run a specific test
go test -run TestName ./package/...
```

## Architecture

### Execution Modes

- **ServerApp** - HTTP web server (`app.ExecType = "server"`)
- **ConsoleApp** - CLI command runner (`app.ExecType = "cli"`)
- **RunMode** - `"dev"` or `"prod"` (affects error handling verbosity)

### Bootstrap Flow

1. Load `.env` file
2. Parse configuration via Viper (`config/config.*`)
3. Set timezone
4. Create IoC container
5. Register all providers (`provider/bootstrap.go`)
6. Boot all providers
7. Start server or execute CLI command

### Provider Pattern

All subsystems are initialized via providers with two phases:
- `Register()` - bind services into the container
- `Boot()` - start services that depend on each other

Providers live in `provider/` and are orchestrated by `provider/bootstrap.go`.

### IoC Container

- Located in `service/container.go`
- Struct tag injection: `container:"inject"`
- Supports Singleton and Transient scopes
- Lazy initialization

## Applications Built on the Framework

The recommended application layout is `docs/PROJECT_STRUCTURE.md`: `cmd/app` (server) and `cmd/cli` (console), one composition root in `pkg/provider` with a server and a console bootstrapper, `db/migration.All()`, and the test, API-contract and deployment practices in `docs/APP_TESTING.md`, `docs/API_CONTRACT.md` and `docs/DEPLOYMENT.md`. `docs/app-template/` is a working copy of it. Anything the framework prints or generates for applications (command hints, templates) should use that layout.

## Directory Structure

```
app/           # Application lifecycle (ServerApp, ConsoleApp)
app/core/      # Core interfaces (IApplication, Router, IAuthContext, etc.)
auth/          # JWT and session-based authentication strategies
command/       # CLI command resolution and built-in commands (migrate, seed, diff)
config/        # Viper config parser
db/            # Database layer
db/migration/  # The framework's own migrations (the sessions table)
db/orm/        # Custom ORM wrapper around GORM; VerifyModel checks a model against its table
db/sql/builder/  # Engine-agnostic query builder; every byte of SQL comes from a dialect
db/sql/config/   # Typed datasource config: flags (external_schema, read_only, lazy_connect), auth
db/sql/core/     # SQL interfaces, conditions and their rendering seam, datasource policy, SQL guards
db/sql/driver/   # Driver-name registry
db/sql/driver/{postgres,mysql,sqlserver}/  # Each registers one engine
db/sql/driver/builtin/  # Registers Postgres and MySQL; never SQL Server
db/sql/driver/sqlserver/azuread/  # Opt-in Entra ID sign-in for SQL Server (links the Azure identity SDK)
db/sql/driver/sqlserver/azuread/persistentcache/  # Opt-in OS-keychain token cache (development)
db/sql/gorm/{postgres,mysql,sqlserver}/v2/  # One engine each: dialect, datasource, session, executor
db/sql/gorm/guard/  # gorm callbacks enforcing read_only and external_schema
db/sql/internal/goldentest/  # Golden-file helpers for the db/sql tests
decoder/       # Query string and multipart form decoders
docs/          # Guides; docs/app-template is a separate Go module, outside ./...
e2e/           # Dockerised e2e harness and its fixture app (cmd/app, cmd/cli)
err/           # Custom error types
event/         # Event bus (pub/sub, sync/async)
http/          # HTTP context, middleware, controllers, router (Chi)
i18n/          # Internationalization manager
job/           # Background job scheduler
log/           # Pluggable logger
mail/          # Email service
model/         # Base domain class, binder, pagination, access control, DTOs
provider/      # Service providers
service/       # IoC container, pagination service, cache
testsupport/   # Database harness for tests (docs/TESTING.md)
util/          # String, reflect, slice, map utilities
validator/     # Validation (wraps go-playground/validator with custom validators)
view/          # Template rendering (Amber, native Go templates)
```

## Key Interfaces

All core contracts are defined in `app/core/`:

| Interface | File | Purpose |
|-----------|------|---------|
| `IApplication` | `app/core/app.go` | Application lifecycle |
| `Router` | `app/core/router.go` | HTTP routing |
| `IAuthContext` | `app/core/auth.go` | Authentication strategy registry/facade |
| `IContainer` | `app/core/ioc.go` | Dependency injection |
| `IDBContext` | `app/core/db.go` | Database operations |
| `IEventBus` | `app/core/event.go` | Event pub/sub |
| `Logger` | `app/core/logger.go` | Logging |
| `IValidator` | `app/core/validator.go` | Validation |

## Code Conventions

- **Interface-driven**: major components defined as interfaces in `app/core/`
- **Domain-Driven Design**: use `model.Domain[T]` as base for domain models
- **RBAC**: field-level and role-based access control via `model/access_control.go`
- **Naming**: camelCase Go, snake_case for DB fields (auto-converted)
- **Error handling**: use types from `err/errors.go`; errors bubble up to registered handlers in `provider/error_provider.go`
- **Middleware**: Chi-based pipeline; apply per-route or globally with include/exclude patterns

## Key Dependencies

| Package | Purpose |
|---------|---------|
| `gorm.io/gorm` + `gorm.io/driver/postgres` | ORM + PostgreSQL |
| `gorm.io/driver/mysql` | MySQL, linked through `db/sql/driver/mysql` or `builtin` |
| `gorm.io/driver/sqlserver` + `github.com/microsoft/go-mssqldb` | SQL Server and Azure SQL, linked only through `db/sql/driver/sqlserver` (opt-in) |
| `github.com/Azure/azure-sdk-for-go/sdk/azidentity` + `azcore` | Entra ID sign-in, linked only through `db/sql/driver/sqlserver/azuread` (opt-in) |
| `github.com/Azure/azure-sdk-for-go/sdk/azidentity/cache` | Persistent Entra token cache, linked only through `db/sql/driver/sqlserver/azuread/persistentcache` (opt-in) |
| `github.com/go-chi/chi/v5` | HTTP router |
| `github.com/go-playground/validator/v10` | Struct validation |
| `github.com/golang-jwt/jwt/v5` | JWT auth |
| `github.com/spf13/viper` | Configuration |
| `github.com/joho/godotenv` | `.env` loading |
| `github.com/google/uuid` | UUID generation |
| `github.com/stretchr/testify` | Test assertions |
| `github.com/eknkc/amber` | Amber templates |

`db/sql/driver/builtin/split_test.go` keeps the opt-in dependencies opt-in: `builtin`,
`provider`, `auth`, `command/db`, `db/migration`, `db/orm` and `testsupport` link no SQL Server
code, `driver/sqlserver` links no Azure SDK, and `driver/sqlserver/azuread` no keychain. Framework
code picks SQL Server behaviour by dialect name (`"sqlserver"`), never by importing the engine.

## Authentication

Two built-in strategies (both implement `core.IAuthStrategy`):

- **JWT** (`auth/jwt_auth_strategy.go`) - stateless, token-based
- **Standard** (`auth/standard_auth_strategy.go`) - session-based (memory or DB-backed)

`IAuthContext` (`app/core/auth.go`) is the registry/facade that holds and resolves the active strategy; it is not implemented by the strategies themselves.

Session storage options: `auth/memory_session.go`, `auth/db_session.go`

Built-in middleware: `http/middleware/auth_middleware.go`, `http/middleware/jwt_middleware.go`, `http/middleware/csrf_middleware.go`

## Database & Migrations

- Migrations implement `core.IMigration` (`app/core/command.go`) with `Up()` / `Down()` methods
- Run via the application's console binary: `go run ./cmd/cli db:migrate up`, `go run ./cmd/cli db:migrate down`, `go run ./cmd/cli db:diff`, `go run ./cmd/cli db:seed`
- Seeders implement `core.ISeeder` (`app/core/command.go`)
- `db.Migration` and `db.Seeder` are ORM models that track executed migrations/seeds in the database, not the extension-point interfaces

## Testing Notes

- Test files use `testify/assert` and standard `testing` package
- Mock auth context available at `model/mock_auth_context.go`
- Tests exist in most packages; the database ones are in `auth/`, `command/db/`, `db/migration/`, `db/orm/`, `db/sql/...`, `provider/`, `testsupport/` and `e2e/tests/`
- Golden files pin what Postgres and MySQL render: `db/sql/core/testdata/conditions.golden` and `db/sql/builder/testdata/pg_mysql.golden`. A package's `-update` flag rewrites its file (`go test ./db/sql/core -run Golden -update`); a changed golden is changed SQL, so it needs a CHANGELOG entry
- Live tests carry the `livedb` build tag and need running engines (`docs/TESTING.md`): `go test -tags=livedb ./e2e/tests` and `go test -tags=livedb ./testsupport`. `E2E_REQUIRE_LIVE=1` makes the Postgres and MySQL cases fail instead of skip, and `E2E_REQUIRE_SQLSERVER=1` the SQL Server ones; `E2E_REQUIRE_SQLSERVER=1 sh e2e/run.sh` starts SQL Server in the dockerised harness
- The manual Azure SQL test carries the `azuresql` tag, reads `GORGANY_AZURESQL_*` and fails without them; CI only compiles it (`go vet -tags=azuresql ./db/sql/driver/sqlserver/azuread`)
