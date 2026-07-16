# Gorgany Framework - AGENTS.md

## Project Overview

Gorgany is a modular Go web framework for building scalable HTTP servers and CLI applications. It provides a complete, opinionated architecture with authentication, authorization, ORM, validation, events, and more.

**Go version:** 1.24+

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

## Directory Structure

```
app/           # Application lifecycle (ServerApp, ConsoleApp)
app/core/      # Core interfaces (IApplication, Router, IAuthContext, etc.)
auth/          # JWT and session-based authentication strategies
command/       # CLI command resolution and built-in commands (migrate, seed, diff)
config/        # Viper config parser
db/            # Database layer
db/orm/        # Custom ORM wrapper around GORM
db/sql/gorm/postgres/v2/  # PostgreSQL dialect (query builder, executor)
decoder/       # Query string and multipart form decoders
err/           # Custom error types
event/         # Event bus (pub/sub, sync/async)
http/          # HTTP context, middleware, controllers, router (Chi)
i18n/          # Internationalization manager
job/           # Background job scheduler (gocron)
log/           # Pluggable logger
mail/          # Email service
model/         # Base domain class, binder, pagination, access control, DTOs
other/         # Request/response/session/view scopes
provider/      # Service providers
service/       # IoC container, pagination service, cache
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
| `github.com/go-chi/chi` | HTTP router |
| `github.com/go-playground/validator/v10` | Struct validation |
| `github.com/golang-jwt/jwt/v5` | JWT auth |
| `github.com/spf13/viper` | Configuration |
| `github.com/joho/godotenv` | `.env` loading |
| `github.com/google/uuid` | UUID generation |
| `github.com/stretchr/testify` | Test assertions |
| `github.com/jasonlvhit/gocron` | Job scheduling |
| `github.com/eknkc/amber` | Amber templates |

## Authentication

Two built-in strategies (both implement `core.IAuthStrategy`):

- **JWT** (`auth/jwt_auth_strategy.go`) - stateless, token-based
- **Standard** (`auth/standard_auth_strategy.go`) - session-based (memory or DB-backed)

`IAuthContext` (`app/core/auth.go`) is the registry/facade that holds and resolves the active strategy; it is not implemented by the strategies themselves.

Session storage options: `auth/memory_session.go`, `auth/db_session.go`

Built-in middleware: `http/middleware/auth_middleware.go`, `http/middleware/jwt_middleware.go`, `http/middleware/csrf_middleware.go`

## Database & Migrations

- Migrations implement `core.IMigration` (`app/core/command.go`) with `Up()` / `Down()` methods
- Run via CLI: `go run . db:migrate up`, `go run . db:migrate down`, `go run . db:diff`, `go run . db:seed`
- Seeders implement `core.ISeeder` (`app/core/command.go`)
- `db.Migration` and `db.Seeder` are ORM models that track executed migrations/seeds in the database, not the extension-point interfaces

## Testing Notes

- Test files use `testify/assert` and standard `testing` package
- Mock auth context available at `model/mock_auth_context.go`
- Tests exist in: `auth/`, `db/orm/`, `db/sql/gorm/postgres/v2/`, `http/`, `model/`, `service/`
