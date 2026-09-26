# Project structure

The framework has opinions about where an application's files are, and until now they were
written down nowhere. It resolves `.env`, `config/config.*`, `resource/` and `db/migration`
against the process's working directory. `domains:register` writes `package provider` into
`pkg/provider/domains.go`. The generator puts its output in fixed `pkg/…/generated`
directories. Applications that grow up without a map tend to drift in the same ways:

- **Entry points take several shapes.** One of them, two `package main` files side by side
  in `cmd/`, fails `go build ./...`, `go vet ./...` and `go test ./...`, so CI cannot run the
  standard commands at all.
- **The console shares the server's provider list**, so `cli db:migrate up` starts the job
  scheduler.
- **Migrations are registered inline in a provider**, so tests cannot run the same list.
- **Suites stop at unit tests**, and each database test invents its own opt-in variable.

This document is a layout that satisfies the framework and the Go tooling together.
[app-template/](app-template/) is a minimal working instance of it: every file it has
follows this document, and directories it does not need yet are left out. It builds and
lints clean, and its unit, integration and e2e tiers pass. The e2e tier runs against the
template's own production image. The gorgany-cli generator does not scaffold this layout
yet. "Adopting this in an existing application" below converts what it produces.

Companion documents:

| Document | What it covers |
|----------|----------------|
| [APP_TESTING.md](APP_TESTING.md) | Test tiers, route-contract tests, the integration tier, the e2e harness |
| [API_CONTRACT.md](API_CONTRACT.md) | `api/openapi.yaml`, the route inventory, the envelope, versioning |
| [DEPLOYMENT.md](DEPLOYMENT.md) | The image, ignore files, configuration, the Makefile, CI, releases, health |

## Starting a new service from the template

The template's short name and its module path are both `myapp`. Replace both before the
first commit.

1. Copy it out of the framework repository:
   `git clone --depth 1 https://github.com/osbits/gorgany /tmp/gorgany`, then
   `cp -R /tmp/gorgany/docs/app-template billing && cd billing && git init`.
2. Set the module path; slashes are fine:
   `go mod edit -module example.com/acme/billing`, then rewrite the imports with
   `grep -rl '"myapp/' --include='*.go' . | xargs sed -i.bak 's#"myapp/#"example.com/acme/billing/#g'`
   and `find . -name '*.go.bak' -delete`. Two of those files compile only under a test or
   a build tag (`pkg/provider/routes_test.go` and `pkg/service/note_service_integration_test.go`).
   `go build ./...` does not see them; `make verify` does.
3. Replace the short name wherever `grep -rn myapp .` still finds it:
   - `IMAGE_REPO` in the `Makefile`; the image tag and the default compose project name in
     `test/e2e/run.sh`; the image tag in `test/e2e/compose.yaml`. Image tags are global to
     the Docker host, so two services that both keep `myapp:e2e` test each other's image.
   - `/etc/myapp`, `/srv/myapp` and `/var/backups/myapp` in `deploy/compose.prod.yml`,
     `.gitlab-ci.yml` and `docs/runbooks/`.
   - The database identity in `.env.sample` and `test/e2e/.env.e2e`, and its fallbacks in
     `compose.yaml` (`${DB_USER:-myapp}`, `${DB_NAME:-myapp_dev}`).
   - The module path in the doc comment of `pkg/buildinfo/buildinfo.go`, which step 2's
     import rewrite does not reach.
   - The titles in `README.md`, `docs/README.md` and `api/openapi.yaml`.

   Afterwards, `grep -rn myapp .` prints nothing.
4. `go get github.com/osbits/gorgany/v2@latest && go mod tidy`.
5. Replace the notes example when your first feature lands. It touches:
   - `pkg/domain/note.go`, `pkg/service/note_service*.go`, `pkg/controller/api/v1` and
     `pkg/model/command/json`;
   - `db/seeder/welcome_note_seeder.go` and `seeder.All()`;
   - the `notes` table in the first migration. Keep `users`: `UserService` reads it. The
     migration is unreleased, so edit it freely;
   - `routes.go` and `app_provider.go`;
   - `pkg/provider/domains.go`: delete the `domain.Note` line by hand first, because the
     console does not compile while the file names a type that is gone. Once your new entity
     exists, start the dev database and run `go run ./cmd/cli domains:register` (see
     "Generated code" for the v2.2.1 caveats);
   - `publicAPI` in `routes_test.go`, the `paths` in `api/openapi.yaml`, and `api/routes.txt`
     (`go test ./pkg/provider -run TestRouteInventory -update`);
   - `test/e2e/notes_test.go`; `test/e2e/security_test.go`, whose three tests POST to
     `/api/v1/notes` (point them at your first signed-in, mutating route); and the migration
     and seeder names in `test/e2e/cli_state_test.go`.
6. Rewrite the first paragraph of `README.md`, `docs/architecture.md`, the `[Unreleased]`
   section of `CHANGELOG.md`, the contact in `SECURITY.md`, the groups in
   `.gitlab/CODEOWNERS` and the date in `docs/adr/0001-*.md`.
7. Do what the header of `.gitlab-ci.yml` lists: the two runners, protected `v*` tags, the
   three protected deploy variables, and a daily pipeline schedule.
8. `make verify lint lint-api test test-integration e2e`, then commit.

## The layout

```
myapp/                            module root = repository root = process working directory
├── cmd/
│   ├── app/main.go               HTTP server   → bin/app, /app/app
│   └── cli/main.go               console       → bin/cli, /app/cli
├── pkg/
│   ├── provider/                 composition root; package name must be `provider`
│   │   ├── bootstrap.go          NewServerBootstrapper, NewConsoleBootstrapper, the shared list
│   │   ├── app_provider.go       the app's bindings: IUserService, renderer, services
│   │   ├── route_provider.go     filters (session, CSRF, same origin), 404 handler
│   │   ├── routes.go             controllers(): every controller, as a plain function
│   │   ├── routes_test.go        route inventory, handler resolvability, sign-in coverage, spec parity
│   │   ├── domain_provider.go    registers generatedDomains() with the framework
│   │   ├── domains.go            written by `go run ./cmd/cli domains:register`
│   │   ├── database_provider.go  migration.All(), seeder.All()
│   │   ├── command_provider.go   console only
│   │   ├── job_provider.go       server only
│   │   ├── error_provider.go     error type → response
│   │   ├── controllers.go        generated: GeneratedControllers(), when the generator is used
│   │   └── <capability>_provider.go
│   ├── buildinfo/                Version, set with -ldflags
│   ├── constant/    [generated/] route namespaces, the /v1 prefix, enums
│   ├── domain/      [generated/] entities, and nothing else
│   ├── model/
│   │   ├── dto/     [generated/] response shapes
│   │   └── command/{json,multipart,params}/ [generated/], command/query/   request shapes
│   ├── service/     [generated/] use cases and their errors; service/dto builds responses
│   ├── controller/  [generated/] server-rendered control panel, if any
│   │   └── api/v1/  [generated/] JSON API
│   ├── health/                   /healthz, /readyz, and the container probe
│   ├── middleware/               the app's IMiddleware types
│   ├── auth/                     auth strategies and decorators
│   ├── command/                  console commands; the template's `version` replaces the framework's
│   ├── job/                      scheduled jobs
│   ├── event/                    event names, payload types and their context accessors
│   ├── subscriber/               event subscribers
│   ├── mail/                     one type per email
│   ├── adapter/<vendor>/         clients for external systems
│   ├── <capability>/             a cohesive feature: billing, notification, search
│   └── grgcompat/                framework workarounds and their canary tests
├── db/
│   ├── migration/                registry.go (All()), one file per migration
│   └── seeder/                   registry.go (All()), reference data only; fixture/ for dev data
├── resource/
│   ├── view/                     *.gohtml, when the app renders templates
│   ├── i18n/<lang>.yaml|json     read only when NewI18nProvider() is registered
│   ├── public/                   static assets; public/storage/ receives stored uploads
│   └── temp/                     runtime only, ignored
├── config/config.yml             the only config file
├── scheme/gorgany.json           generator input, if the app uses the generator
├── api/
│   ├── openapi.yaml              the HTTP contract
│   └── routes.txt                every route the server answers, kept current by a test
├── test/
│   ├── arch/                     import rules and repository invariants
│   ├── testkit/                  helpers imported only by tests
│   └── e2e/                      black-box suite and its compose harness
├── docs/                         see "Documentation in the repository"
├── deploy/compose.prod.yml       production stack, with the one-off backup, migrate and seed services
├── compose.yaml                  local dependencies only
├── .gitlab/CODEOWNERS            who approves the files that decide security, contract and releases
├── Dockerfile  .dockerignore  .gitignore  .env.sample  .golangci.yml  redocly.yaml  .editorconfig
├── Makefile  .gitlab-ci.yml
├── README.md  AGENTS.md  CLAUDE.md  CHANGELOG.md  SECURITY.md
└── go.mod  go.sum
```

Application code stays in `pkg/`, not `internal/`. Nobody imports an application module, so
`internal/` would protect nothing. The generator, meanwhile, hard-codes `pkg/` into every
import it writes, and `domains:register` and `db:diff` scan `./pkg/domain`.

## The two binaries

**One directory per binary.** Two `main` functions in one package is a compile error, so
`cmd/app.go` next to `cmd/cli.go` only builds file by file, with `go build cmd/app.go`. Then
`go build ./...`, `go vet ./...` and `go test ./...` fail on `cmd` with `main redeclared in
this block`, and no CI gate built on them can ever pass. A server that is the `cmd` package
itself, with the console in `cmd/console`, builds, but `go install ./cmd/...` then produces
binaries called `cmd` and `console`. With `cmd/app` and `cmd/cli`, the directory name is the
binary name everywhere, from `go build -o bin/ ./cmd/app ./cmd/cli` to `/app/app` in the
image. The Dockerfile, CI and docs then have nothing left to disagree about.

```go
// Command app is the HTTP server.
//
// Start it with the project root as the working directory: the framework reads
// .env, config/config.* and resource/ relative to it. In the image that is
// WORKDIR /app.
//
//	app              serve HTTP on app.server.port
//	app healthcheck  ask the running server whether it is ready; exit 0 or 1
package main

import (
	"os"

	// The IANA zone database, compiled in: TZ and time.LoadLocation work the same in
	// a scratch image as on a laptop.
	_ "time/tzdata"

	"github.com/osbits/gorgany/v2/app"

	"myapp/pkg/health"
	"myapp/pkg/provider"
)

func main() {
	// The container probe must not boot the application: a boot opens the database
	// pools and runs every provider, and a HEALTHCHECK does that every few seconds.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(health.Probe())
	}

	app.NewServerApp(provider.NewServerBootstrapper()).Run()
}
```

```go
// Command cli runs one console command and exits.
//
//	cli db:migrate up [--datasource=name]
//	cli db:migrate down [--datasource=name] [--steps=n]
//	cli db:seed
//	cli version                         the version the build stamped
//	cli <area>:<verb> [--flag=value]    the app's own commands, in pkg/command
//
// Same working-directory rule as cmd/app. db:migrate and db:seed exit non-zero on
// failure. db:diff and session:gc print the failure and exit 0, so a job that runs
// them checks their output as well as the exit status.
package main

import (
	_ "time/tzdata"

	"github.com/osbits/gorgany/v2/app"

	"myapp/pkg/provider"
)

func main() {
	app.NewConsoleApp(provider.NewConsoleBootstrapper()).Run()
}
```

- **`main` holds no logic.** Everything a test needs to reach lives in `pkg/`. The one
  exception is `app healthcheck`. A `scratch` image has no `curl`, and a probe that booted
  the console would open database pools on every interval. `health.Probe` sends one GET to
  `/readyz` on the loopback interface and reads nothing but `SERVER_PORT`.
- **`time/tzdata` belongs in `main`.** That is the Go convention for it. It also matters here,
  because the framework reads `app.server.timezone` but discards a `LoadLocation` error and
  never applies the location to `time.Local`.
- **Every console command boots the full shared provider set, database connections
  included.** Even `cli version` needs a reachable database. Keep this in mind when you write
  image smoke tests.
- **Both binaries start from the project root.** Nothing in `app.Run()` accepts a root
  directory. A binary started anywhere else panics on the missing `.env` first, and on the
  missing `config/config.*` if a `.env` happens to be there.

## The composition root: `pkg/provider`

`pkg/provider` is the only package that knows every other package. It is also where
configuration is read. The one exception is `health.Probe`, which runs before any boot and
reads only `SERVER_PORT`. `cmd/*` imports it, and nothing else does.

```go
// NewServerBootstrapper is what cmd/app boots: the shared providers plus the job
// scheduler, which only the long-running server may start.
func NewServerBootstrapper() core.Bootstrapper {
	return deferred(func() []core.IProvider {
		return append(sharedProviders(), newJobProvider())
	})
}

// NewConsoleBootstrapper is what cmd/cli boots: the shared providers plus the
// console commands. It leaves JobProvider out on purpose: JobProvider.Boot starts
// the scheduler unconditionally, so a console that included it would run cron
// jobs in the middle of `db:migrate up`.
func NewConsoleBootstrapper() core.Bootstrapper {
	return deferred(func() []core.IProvider {
		return append(sharedProviders(), newCommandProvider())
	})
}

// sharedProviders is the order both binaries boot in. Every Register runs in this
// order, then every Boot in the same order.
func sharedProviders() []core.IProvider {
	return []core.IProvider{
		grgprovider.NewLoggerProvider(),
		newErrorProvider(),
		grgprovider.AppProvider{}, // auth context, session storage, the "default" and "api" strategies
		grgprovider.NewDbProvider(),
		newDomainProvider(),   // generatedDomains(), for db:diff and domains:register
		newDatabaseProvider(), // migration.All(), seeder.All()
		newRouteProvider(),    // the console needs it too: it binds IWebContext and RouteLinker
		&AppProvider{},        // last: the app's bindings win over the framework's
	}
}

// deferred builds the provider list inside Bootstrap. The framework calls
// Bootstrap only after it has loaded .env and config/config.*, so a provider
// constructor that reads configuration sees real values. The same constructor
// called as an argument to app.NewServerApp would run first and see nothing.
type deferred func() []core.IProvider

func (build deferred) Bootstrap(container core.IContainer) {
	bootstrapper := grgprovider.NewGorganyBootstrapper()
	for _, p := range build() {
		bootstrapper.AddProvider(p)
	}
	bootstrapper.Bootstrap(container)
}
```

**Why `deferred`.** `app.NewServerApp(provider.NewBootstrapper())` evaluates its argument
before `Run()` has loaded `.env` or parsed the config. A provider constructor that reads viper
there reads an empty configuration. Nothing fails; the constructor builds with empty values.
The template's `newRouteProvider` shows why that matters. It reads `app.server.url` to tell
the same-origin check which origin is its own. Built eagerly, it would read `""`. The
template panics on an empty value rather than let the check fall back to trusting the
request's `Host` header, so built eagerly it would never boot. A CORS middleware built the same way is
worse: it treats an empty `AllowedOrigins` as every origin, so an allowlist read too early
becomes `Access-Control-Allow-Origin: *`. `deferred` removes the trap without giving up
constructor functions. A security setting should still fail closed: check it, and panic at boot when it
is empty. The framework does this itself only for `auth.jwt.secret` and
`auth.session.cookie.secure`.

**Why two lists, and why these differences.**

- **`JobProvider` is server-only.** Its `Boot` starts the scheduler unconditionally. The
  framework offers no execution-mode accessor for a provider to consult. The only signal is
  the concrete type of the global `app.Application`, and that is no basis for wiring.
- **`CommandProvider` is console-only.** Its `Register` binds the console context, which only
  `ConsoleApp` resolves.
- **`RouteProvider` is shared.** It binds `IWebContext` and `RouteLinker`, which commands and
  mail builders need to produce URLs. It also validates every handler's parameters at boot,
  so a mistake there fails `db:migrate` as well as the server and cannot reach a deploy
  unnoticed.

**Rules for this package.**

- **The package must be called `provider`.** `domains:register` writes `package provider` into
  `pkg/provider/domains.go`. Import the framework's package as `grgprovider`, so the
  framework's `AppProvider{}` and the app's `&AppProvider{}` read as different things.
- **The database driver's blank import lives here**, next to the bootstrapper:
  `_ "github.com/osbits/gorgany/v2/db/sql/driver/postgres"`. `DbProvider` registers no
  driver of its own (MIGRATION_v2 §21). Without the import, the app compiles and then refuses
  to boot.
- **Two bindings are mandatory, even in an API-only app.** Bind `core.IUserService`, because
  both built-in auth strategies inject it. Bind a `core.IEngineRenderer`, because `http.Message`
  injects one. An app that renders no templates binds a no-op renderer. One that does
  adds `grgprovider.NewViewProvider()`.
- **`controllers()` is a plain function**, not a container lookup, so the route-contract tests
  can walk it without booting anything (APP_TESTING.md).
- **A binding that fails to register panics the boot.** A process that starts without it fails
  later, on a request, far from the cause.

## What goes where

| Path | Holds | Must not hold |
|------|-------|---------------|
| `cmd/app`, `cmd/cli` | One `main.go` each | Flags, configuration reads, wiring, a third binary. A new task is a console command |
| `pkg/provider` | Wiring, configuration reads, the driver import, one file per extension point, route-contract tests | Handlers, business rules, middleware types |
| `pkg/domain` | Entities: the generated base plus a hand-written wrapper with the same file name, relations, `TableName` | Anything that is not an entity. `domains:register` and `db:diff` treat **every struct here** as a table, so error types, value objects and helpers live elsewhere. Service calls, request or response types, HTTP |
| `pkg/model/**` | Request commands (`ContentType()`, `validate` tags) and response DTOs | Imports of `pkg/service`; authorisation. A request DTO that asks a service locator whether the caller may proceed hides access control where no route test sees it |
| `pkg/service/**` | Use cases, embedding the generated CRUD service when there is one; their error types, such as `NotFoundError`; response building in `service/dto` | `core.HttpMessage`, vendor HTTP clients, one-off data-migration code. The generator's `pkg/service/*_hateos_service.go` builds links from the request; leave it where the generator puts it |
| `pkg/controller/**` | `GetRoutes()` and handlers. Per-route `AuthMiddleware` in `RouteConfig.Middlewares` | SQL, business rules, event publishing. Publish from services |
| `pkg/health` | Liveness, readiness, `Probe()` | Anything that needs a booted container in `Probe()` |
| `pkg/middleware` | `core.IMiddleware` implementations | Route registration |
| `pkg/command` | `core.ICommand` implementations. A command that changes data takes a dry-run flag and runs in a transaction | Logic duplicated from a service or job |
| `pkg/job` | `core.IJob` implementations, registered as pointers when they inject anything | Jobs nothing registers. Delete them |
| `pkg/event`, `pkg/subscriber` | Event name constants, one payload type per event with its context accessors; one subscriber per file, and one subscriber per event name | String literals at publish sites |
| `pkg/adapter/<vendor>` | A client for one external system, with an injectable `http.Client` | Domain rules; package-global client registries |
| `pkg/<capability>` | A cohesive feature: a pure service, a `Store` interface, its Postgres implementation, its tests. Wired by `pkg/provider/<capability>_provider.go` | HTTP handlers, wire DTOs |
| `pkg/grgcompat` | Workarounds for framework defects, each with a canary test | Anything not tied to a named framework defect |
| `db/migration` | One migration per file, `registry.go` | Imports of `pkg/`. Migrations are frozen history |
| `db/seeder` | Reference data every environment needs; `fixture/` for development and e2e data | Real personal data, credentials, working-directory-relative file reads |
| `config/` | `config.yml` | Per-environment copies (the framework reads one file), literal secrets |
| `resource/` | Views (`.gohtml` only; nothing else is loaded), translations, static assets | Uploads outside `public/storage` (a volume in production), binaries |
| `api/` | The contract and the route inventory | Generated clients |
| `test/arch` | Import rules, toolchain pins, env-sample completeness | Tests that need a database |
| `test/testkit` | Fakes, builders, the golden-file helper | Imports from non-test code |
| `test/e2e` | The black-box suite and its harness | Imports of `pkg/` |
| `scripts/` | Tracked, read-only helpers that take the target URL as a required argument | Anything that changes data (make it a console command), production default URLs |

Naming:

- **Directory name = package name.** The generator imposes two exceptions.
  `pkg/service/dto/generated` declares `package dto`. `pkg/model/command/json` declares
  `package json` and shadows the standard library, so import it with an alias, as the
  template does (`command "myapp/pkg/model/command/json"`).
- **No grab-bag packages** (`util`, `common`, `helpers`). Name a package after what it provides.
- **No new package names that shadow a builtin or the standard library** (`error`, `log`,
  `http`). Where an app package shares a framework package's name, alias the framework import
  `grg<name>`, as with `grgprovider`.
- **Route names are `<namespace>.<resource>.<action>`**, such as `api.note.show`. A route outside a
  namespace drops that segment (`health.live`). Names are unique: the router's named-route map
  is last-writer-wins, so a duplicate silently repoints every URL built from that name. The
  route inventory test rejects duplicates.
- **File names are snake_case, one primary type per file.** An extension file has the same
  name as its generated base. Do not give new files an `_ext` suffix.

## Generated code

If the application uses the gorgany-cli generator, its files fall into four kinds:

| Kind | Where | Rule |
|------|-------|------|
| Generator-owned | everything under `generated/`, and `pkg/provider/controllers.go` | **Never edit.** They are recorded in `grg-lock.json`, so a lock check notices an edit. Change behaviour in the extension file, or upstream in the template |
| Rewritten on every run, outside `generated/` | `pkg/service/*_hateos_service.go`, `pkg/service/dto/*_dto_builder_service.go`, `pkg/model/dto/dto_hateos.go`, `resource/i18n/*.json` | **Never edit.** They carry no generator header and are not in `grg-lock.json`, so nothing notices an edit before the next run replaces it |
| Rewritten by a tool or by `-whole` | `pkg/provider/domains.go` (`domains:register` rewrites it whole); and, under `regenerate-project -whole`, `pkg/controller/index_controller.go`, `pkg/service/auth_user_service.go`, `resource/view/index.gohtml`, `resource/view/auth/login.gohtml`, `resource/public/lte/`, `scheme/postman_scheme.json` | Keep hand-written code out of them, or never run `-whole` |
| Yours | extension files one level above `generated/`, `cmd/`, the other `pkg/provider` files, `config/`, `db/` | Written once by `create-project`. A default run leaves them alone. `-withExtensions`, `-withDto`, `-withController` and `-withService` rewrite the extension files. So does any run that cannot read `grg-lock.json`, which then treats every domain as new: pass `-path` as an absolute path |

Rules that follow:

- **Regeneration never compares a file with `grg-lock.json`.** It overwrites locked files as
  readily as the rest. It reads the lock only to find domains that are new since the last run,
  and locked files that are now stale.
- **Hand-written code never goes into a `generated/` directory, and never carries a
  `Code generated … DO NOT EDIT.` header.** The header is not cosmetic. golangci-lint skips
  every file whose header matches the Go convention, so a security fix pasted into generated
  controllers under that header is code nothing checks. `exclusions.generated: strict` in the
  template's `.golangci.yml` does not change that. It stops only looser markers, such as
  "autogenerated", from exempting a file.
- **Security logic never lives in generated files.** It lives in the framework, in the
  generator's templates, or in `pkg/middleware` and `pkg/grgcompat`. A fix patched into
  generated output disappears on the next regeneration.
- **`grg-lock.json` detects edits; it does not prevent them.**
  - While the app still regenerates, check it in CI with a test that recomputes the MD5 of
    every `Files` entry, or by booting once with `app.gorgany.validate: true`, which exits `1`
    on a mismatch.
  - `gorgany-cli validate-project` cannot gate a pipeline: it prints mismatches and exits `0`.
  - A production image ships neither the lock nor the sources, and with no lock file the boot
    check passes without checking anything.
- **While the app regenerates, `resource/i18n` has no hand-maintained file.** The framework
  reads one file per locale and finds `<lang>.json` before `<lang>.yaml`, and the generator
  rewrites `en.json` and `uk.json` on every run. Keys you add, including `validation.<rule>`
  messages, go into the generator's i18n template. Otherwise re-apply them after every run and
  review the diff.
- **After adding a domain, run `go run ./cmd/cli domains:register`.** Partial regeneration does
  not update `pkg/provider/domains.go`, and `domain_provider.go` must call the
  `generatedDomains()` it writes. `domains:register` and `db:diff` read their templates from the
  framework's source tree, so run them with `go run` in a checkout, never from a built binary.
  `domains:register` rewrites the file only when it finds a new entity. Remove a deleted
  entity's line by hand. Up to and including v2.2.1, it writes the file unformatted, with a
  comment that names `./cmd/cli.go`, and creates it with mode `0777` before the umask. After
  it, run `gofmt -w pkg/provider/domains.go && chmod 644 pkg/provider/domains.go`, and
  restore the comment to ``// Run `go run ./cmd/cli domains:register` ``.
- **An application that has stopped regenerating should say so.**
  1. Record it in an ADR.
  2. Archive `scheme/gorgany.json`. Leave `grg-lock.json` in place until step 4 is done: with
     no lock file, an accidental `regenerate-project` treats every domain as new and
     overwrites every extension file, which is the hand-written code. Neither file stops a
     run, though. `regenerate-project` takes the scheme as a flag.
  3. Replace the generator headers with one that says the file is hand-maintained. Remove
     `".*/generated/.*"` from `linters.exclusions.paths` in `.golangci.yml` at the same time,
     so the now hand-maintained code is linted.
  4. Add "never run `gorgany-cli regenerate-project` here" to `AGENTS.md`, and make review
     the guard.

  The `generated/` directory names can stay. Renaming them churns imports across most of
  the repository for no runtime gain.

## Import rules

A package may import only what its layer allows:

```
cmd/*                  → pkg/provider, pkg/health
pkg/provider           → everything except test/*             (the composition root)
controller, middleware, command, job, subscriber              (delivery)
                       → delivery, auth, service, <capability>, model, event, domain,
                         adapter, grgcompat, constant, buildinfo
auth                   → service, <capability>, model, event, domain, adapter, grgcompat,
                         constant, buildinfo
service                → <capability>, model, event, domain, adapter, grgcompat, constant, buildinfo
<capability>           → <capability>, model, event, domain, adapter, grgcompat, constant, buildinfo
model, event           → model, event, domain, constant, buildinfo
domain                 → grgcompat, constant, buildinfo
health, adapter, grgcompat → constant, buildinfo
constant, buildinfo    → nothing in this module
db/migration           → nothing in this module
db/seeder              → domain, constant, buildinfo
test/*                 → imported only from _test.go files
```

A package may always import its own subpackages. A `pkg/` package this table does not name,
such as `pkg/mail` or `pkg/<feature>`, is a `<capability>`.

`test/arch/layering_test.go` in the template enforces exactly this. Its `allowed` table is
the policy, and it uses only the standard library, so `go test ./...` fails on a forbidden
import with no linter configured. An existing application starts with its current violations
in `test/arch/known_violations.txt`. The test fails on any edge not listed there, and on any
listed edge that has gone, so the file can only shrink.

Two more rules that the import graph cannot see:

- **No service locator.** `service.EmergencyContainer()` hides a dependency from every reader
  and every test. Inject with `container:"inject"`. The template's lint configuration forbids
  new calls.
- **Configuration is read in `pkg/provider` and handed on as typed values.** `viper.Get*`
  calls scattered through services cannot be listed, defaulted or checked.

## Where each extension point is wired

| Extension point | Code lives in | Wired in `pkg/provider/…` | Framework API |
|-----------------|---------------|---------------------------|---------------|
| Routes | `pkg/controller/**`, `pkg/health` | `routes.go` → `route_provider.go` | `RouteProvider.AddController(core.IController)` |
| Per-route auth | the controller's `GetRoutes()` | – | `&middleware.AuthMiddleware{AuthStrategies: …, Roles: …}` in `RouteConfig.Middlewares` |
| Filters | `pkg/middleware` | `route_provider.go` | `AddMiddleware(grghttp.NewMiddlewareConfigBuilder().WithPattern(…).WithExcludePatterns(…).AsFilter().WithMiddleware(m).Build())` |
| CSRF, same origin | – | `route_provider.go` | A CSRF filter on the cookie-authenticated pattern: `middleware.NewCSRFMiddleware()` ([CSRF.md](CSRF.md)). Plus `RouteProvider.EnableSameOriginProtection(middleware.SameOriginOptions{ServerURL: …})` |
| Not found | `route_provider.go` | `route_provider.go` | `SetNotFoundHandler(core.HandlerFunc)` |
| Migrations, seeders | `db/migration`, `db/seeder` | `database_provider.go` Boot | `core.IDataContext.AddMigration`, `AddSeeder` |
| Console commands | `pkg/command` | `command_provider.go` (console list) | `CommandProvider.AddCommand(core.ICommand)` |
| Jobs | `pkg/job` | `job_provider.go` (server list) | `JobProvider.AddJob(func() core.IJob)` |
| Events | `pkg/event`, `pkg/subscriber` | `event_provider.go`, in `sharedProviders()` | `EventProvider.RegisterSubscriber`, `RegisterAsyncSubscriber`; publish through an injected `core.IEventBus` |
| Error responses | the error type next to the code that raises it | `error_provider.go` | `ErrorProvider.AddHandler("<TypeNameWithoutPackage>", core.ErrorHandler)` |
| Auth strategies | `pkg/auth` | `app_provider.go` Boot | `core.IAuthContext.RegisterAuthStrategy(name, strategy)` |
| Users | `pkg/service` | `app_provider.go` Register | bind `core.IUserService` |
| Validators | `pkg/<capability>` | `app_provider.go` Boot | `core.IValidator.RegisterValidation`. Messages go under `validation.<rule>` in `resource/i18n`, and are read only when `NewI18nProvider()` is registered ([VALIDATION.md](VALIDATION.md)) |
| Translations | `resource/i18n/<lang>.yaml` | `grgprovider.NewI18nProvider()` in `sharedProviders()` | Reads `i18n.lang.available` and `i18n.lang.default`. The boot panics when the default locale's file is missing |
| Template functions | – | after `NewViewProvider()` | `core.IEngineRenderer.RegisterGlobalFunction` |
| Domains | `pkg/domain` | `domain_provider.go` + generated `domains.go` | `core.IDomainContext.RegisterDomain` (`db:diff` depends on it) |
| Logger | `pkg/adapter/<sink>` | `app_provider.go` Register | Rebind `core.Logger`. `log.SetLoggerFactory` panics on a second call |

**A handler reports an error by panicking with it.** The recovery middleware hands the error to
the handler registered for its type name. The template's `NoteService.Find` returns a
`*service.NotFoundError`; `Show` panics with it; `error_provider.go` turns it into a `404` in
the standard envelope. Panic with `error` values only. A panic with a string puts that string
in the response in every mode (API_CONTRACT.md).

**A handler takes its request DTO by value**, as in `func(message core.HttpMessage, body
command.CreateNote)`. The framework instantiates DTO parameters with `reflect.New(T)`, so a
pointer parameter never satisfies `core.HttpCommand`. The router refuses it at boot, and the
template's `TestHandlersAreResolvable` runs the same check under `go test`.

**A route authenticated by the session cookie sits behind the CSRF filter.** A route that
clients call with a bearer token uses the `api` (JWT) strategy under its own pattern, and
needs no CSRF filter. Keep the two on separate patterns, because the CSRF filter has no
no-session shortcut. The session cookie's `SameSite=Lax` stops cross-site form posts but not
sibling subdomains; the same-origin check closes that.

**Events.** Put `newEventProvider()`, a `*grgprovider.EventProvider`, in `sharedProviders()` as
soon as any service publishes. A console command that calls that service needs a bound
`core.IEventBus` too. Design around four properties of the bus:

- **One subscriber per event name.** A second `RegisterSubscriber` for the same name replaces
  the first without an error. When one event needs two reactions, register one subscriber
  that performs both.
- **The subscriber receives only the context.** `Handle(ctx)` receives nothing but the context,
  and the `args` passed to `Publish` reach nobody. Carry the payload in the context, under an unexported key
  in `pkg/event`, with typed accessors: `event.WithNoteCreated(ctx, p)` and
  `event.NoteCreatedFrom(ctx)`.
- **Publishing an event with no subscriber is an error.** Check what `Publish` returns.
- **Async subscribers run on the publisher's context, and only the server's shutdown waits
  for them.** Publish from a request with `context.WithoutCancel(ctx)`, or the subscriber's
  queries fail once the response is written. The server waits for them after the requests and
  jobs have drained, within `app.server.timeout.shutdown`. The console does not, so a command
  that publishes calls `bus.WaitAsync()` before it returns.

## Migrations and seeders

```go
// All returns every migration in the order `cli db:migrate up` applies them.
//
// Append only. The migrate command runs this list in order and records each Name()
// in the migrations table; it does not sort. A Name() must never change once
// released. File and struct names are free to change.
func All() []core.IMigration {
	return []core.IMigration{
		Migration20260926120000{},
	}
}
```

- **One list, used twice.** `database_provider.go` registers `migration.All()`, and the
  integration tier's `TestMain` passes the same list to `testsupport.AddMigration`. A
  migration registered inline in a provider cannot be reused, and that alone keeps an
  application off the test harness.
- **`cli db:migrate up` also applies two framework migrations**, the sessions table and its
  version column, which `DbProvider.Boot` adds ahead of the app's list. An integration suite
  that exercises database-backed sessions adds `NewSessionsMigration()` and
  `NewSessionsVersionMigration()` from `github.com/osbits/gorgany/v2/db/migration` ahead of
  `migration.All()`.
- **`registry_test.go` fails on a migration file that `All()` does not list, and on a duplicate
  `Name()`.** An unlisted migration never runs, and nothing else notices.
- **`Name()` is the key in the `migrations` table.** Renaming it runs the migration again. File
  names are free: name new files `YYYYMMDDHHMMSS_<what>.go`, and the struct
  `Migration<14 digits>`.
- **`db:diff`'s output is a draft.** It writes `<timestamp>_migration.go` into `db/migration`.
  Before committing it:
  - rename the file and keep its `Name()`;
  - run `gofmt -w` on it;
  - replace each `sql.Exec("…")` with `dbGorm.Exec("…").Error`. As generated, the statements
    run on the connection pool, outside the transaction `db:migrate` opened, so a failure
    leaves the earlier statements applied and unrecorded;
  - write a real `Down()`: the generated one returns `nil`;
  - append the struct to `All()`.
- **Migrations import nothing from the module.** A migration that calls into `pkg/service`
  changes meaning whenever the service does. Copy what it needs into the file.
- **A failing migration exits non-zero, and later migrations do not run.** That is what lets a
  deploy stop at the migrate step (DEPLOYMENT.md).
- **`Down()` either reverses the migration or returns an error that says it cannot.** It never
  returns `nil` for a no-op.
- **Seeders in `All()` are reference data** that every environment needs, production included.
  Each runs once per `Name()`. They are not atomic, so keep each one small.
- **Development and e2e fixtures** live in `db/seeder/fixture` and are registered only when a
  config flag says so. The flag is not `MODE`, because e2e runs with `MODE=prod`. A convention
  that fails closed is `seed.fixtures: ${SEED_FIXTURES}` in `config.yml`, set to `true` in
  `.env.sample` and `.env.e2e` and unset in production, read in `database_provider.go`:
  ```go
  if viper.GetBool("seed.fixtures") {
  	for _, s := range fixture.All() {
  		data.AddSeeder(s)
  	}
  }
  ```
  An unresolved placeholder is empty, and `GetBool` reads empty as false.
- **Seed data is synthetic and embedded.** Load it with `//go:embed`. The production image
  contains no `db/` directory for a working-directory-relative `os.ReadFile` to find. Real
  people's data never goes into a repository. Large datasets are artefacts that a console
  command imports.

## Framework workarounds

When the application works around a framework defect, put the workaround in
`pkg/grgcompat/<name>/`. Its package comment names the defect, the framework version, and the
condition for removing it. Pair it with a canary test in `pkg/grgcompat` that asserts the
defect is **still present**. When an upgrade fixes the defect, the canary fails. That is the
signal to delete the workaround and then the test. A workaround with no canary outlives its
reason.

Workarounds that already exist elsewhere can stay where they are, with a canary and a package
comment. Moving them churns imports for no benefit.

## Documentation in the repository

| File | Purpose | Kept honest by |
|------|---------|----------------|
| `README.md` | What the service is, a five-command quickstart, the make targets, a layout table | Review, whenever a target or setup step changes |
| `AGENTS.md` | Rules for anyone changing the repository, human or agent: where things go, what never to do, what to run before finishing | Review |
| `CLAUDE.md` | Exactly one line, `@AGENTS.md` | – |
| `CHANGELOG.md` | Keep a Changelog, with **Deployment notes** per release: migrations, new variables, manual steps, and whether rollback needs a restore | Review |
| `SECURITY.md` | How to report a vulnerability, how fast you answer, supported versions, where remediation records live | – |
| `.gitlab/CODEOWNERS` (`.github/CODEOWNERS` on GitHub) | Who must approve changes to the files that decide security, the contract and releases | The platform's "require code owner approval" on the default branch. On GitLab that is a Premium feature; on Free the file only suggests reviewers, so let only Maintainers merge |
| `docs/README.md` | The index: "Document \| What it covers" | – |
| `docs/architecture.md` | Purpose and boundaries, the two binaries, the boot order, data, background work | Review |
| `docs/configuration.md` | Every environment variable (who reads it, default, required in production, secret or not), and the config keys with fixed values | `test/arch` checks `.env.sample` against `config.yml` |
| `api/openapi.yaml`, `api/routes.txt` | The HTTP contract, and every route | `pkg/provider/routes_test.go` and `make lint-api` (API_CONTRACT.md) |
| `docs/runbooks/` | Preparing a host, deploying, rolling back, rotating a secret, handling an incident | Followed at every release and incident, and fixed in the same change as the step it describes |
| `docs/adr/NNNN-<decision>.md` | One decision per file: context, decision, consequences | Immutable once accepted; superseded, not edited |
| `docs/domain/` | Business specifications, one per area | Link to operations in the spec; do not copy endpoint lists |
| `docs/security/YYYY-MM-DD-<topic>.md` | Reviews and remediation records | **Tracked**, like code |

Two rules keep the set true:

- **Say each fact in one place.** Agent files and READMEs drift fastest when they restate the
  Go version, config values, a CORS list or a migration signature. Link to `go.mod` and
  `config/config.yml` instead.
- **Feature documents link to the spec rather than listing endpoints.** A copied endpoint list
  or response shape is the first thing to contradict the code.

## Adopting this in an existing application

The steps run in this order. Each is a small change that can be reviewed on its own, and the
first three unblock everything else.

1. **Fix the ignore files first.** A `.gitignore` that lists `cmd/app` or `cmd/cli`, or an
   unanchored `app` or `cli`, silently swallows the new source directories. So does a
   `.dockerignore` that lists `cmd/app` or `cmd/cli`. The patterns were meant for built
   binaries. Anchor them to the root (`/app`, `/cli`), or build into `/bin/` only.
2. **Split `cmd/`.** Choose the move that matches your current layout:
   - `git mv cmd/app.go cmd/app/main.go` and `git mv cmd/cli.go cmd/cli/main.go`;
   - `git mv cmd/console/main.go cmd/cli/main.go`;
   - for a project from a current gorgany-cli, `git mv cmd/server cmd/app`.

   The generator's `pkg/provider/bootstrapper.go` and `db_provider.go` become this layout's
   `bootstrap.go` and `database_provider.go`. Change the Dockerfile from file builds to
   `go build -o /out/ ./cmd/app ./cmd/cli`, and `ENTRYPOINT` to `CMD`, so that a one-off
   `run --rm app /app/cli …` runs the console. Then grep CI and the docs for the old binary
   names. From here on, `go build ./...` and `go vet ./...` pass.
3. **Gate CI on `make verify test`.** Put existing lint findings in a baseline (golangci-lint's
   `new-from-merge-base`) rather than blocking adoption on them.
4. **Split the bootstrapper** into `NewServerBootstrapper` and `NewConsoleBootstrapper` behind
   `deferred`. The console stops starting the scheduler.
5. **Move migrations and seeders into `registry.go`**, and add `registry_test.go`. Inline any
   `pkg/` code a migration imports. Do not rename existing migrations' `Name()`.
6. **Add `test/arch`**, with today's violations in `known_violations.txt`. Add the route tests,
   with today's routes in `api/routes.txt`.
7. **Add the integration tier, then the e2e harness** (APP_TESTING.md).
8. **Write the contract** (API_CONTRACT.md). Start with the envelope and the routes clients use
   most. The spec-parity test can carry an allowlist of undocumented routes that may only
   shrink.
9. **Record in an ADR what did not move**: generated-code drift, workarounds, naming kept for
   compatibility. The next reader then knows it was a decision.
