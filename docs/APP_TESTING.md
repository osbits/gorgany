# Testing an application

[TESTING.md](TESTING.md) documents `testsupport`, the database harness. This document
covers the rest of an application's suite: which tiers it has, where each lives, and how
the e2e harness runs the image you ship.

Suites built without a plan tend to share the same gaps:

- **Every test is a unit test.**
- **Each database test has its own opt-in variable**, documented nowhere that is tracked,
  and skips silently when the variable is missing.
- **The e2e harness, where there is one, runs the application with `go run`**, so it never
  tests the image that ships.
- **CI runs none of it.** With two `main` packages in one directory, `go build ./...`,
  `go vet ./...` and `go test ./...` fail before a test runs.

[app-template/](app-template/) has every tier described here, passing. Its e2e suite runs
against the template's production image.

## Tiers

| Tier | Build tag | Lives in | Needs | Runs in the template's CI |
|------|-----------|----------|-------|---------------------------|
| Static | – | – | – | Every pipeline: `make verify` (`gofmt`, `go vet ./...`, `go vet -tags=integration,e2e ./...`, `go mod tidy -diff`), `make lint`, `make lint-api`, secret detection |
| Unit | none | `x_test.go` next to `x.go`, plus `test/arch` | Nothing | Every pipeline: `go test -race -count=1 ./...` |
| Integration | `integration` | `x_integration_test.go` in the package under test | Postgres through `testsupport` | Every pipeline, against a service container |
| E2E | `e2e` | `test/e2e/` | Docker and the production image | Every pipeline, against the image the `build` job pushed |

`vuln` runs in every pipeline too. Create a daily pipeline schedule in GitLab (Build >
Pipeline schedules) so it also runs when nothing is pushed. If the e2e stage grows slow, a team can move it to merge requests and a nightly
schedule with `rules:`. Deploy must still need it.

**Use build tags, not environment variables.** A test that skips when a variable is unset
reports success on a laptop with no database, and on a CI job whose variable has a typo. A
build tag makes the opt-in explicit, so inside a tagged file a missing database is a
**failure**.

- A directory whose only Go files are tagged is skipped by `go test ./...`, so the untagged
  run stays hermetic.
- `go vet -tags=integration,e2e ./...` keeps the tagged files compiling.

Unit tests are white-box, in the package they test. Use a `package x_test` only for
public-contract checks. **Do not create a top-level `tests/unit` tree.** Tests there cannot
reach unexported code, so they end up testing less than tests next to the code.

## The unit tier

Each of these patterns has caught a real defect in an application. Treat them as the
minimum, not a menu.

- **Pure business rules, extracted from the code that touches the database.** A calculation
  that needs no database gets a table-driven test. Where the input space is dates or money,
  add a property check across a range.
- **SQL shape without a database.** Build the query and assert on its SQL, or open gorm with
  `DryRun: true` and record the statements. A test that installs a global builder factory
  must not call `t.Parallel()`.
- **Handlers through fakes.** A controller that takes its collaborators as fields can be
  driven with fakes that embed the framework interface and implement only what the test
  uses. Any other call then fails loudly on a nil method.
- **Authorisation matrices.** For every role × handler × owner combination, assert the status
  **and** that nothing changed. A 403 that still wrote the row is the bug.
- **Payload lists** for anything that reaches SQL or the filesystem: sort and filter fields,
  search terms, path segments (`..`, encoded separators, NUL).
- **Golden files for response shapes.** Marshal the DTO through the envelope and compare the
  result with `testdata/<name>.golden.json`, rewritten with `-update`. A dropped or renamed
  JSON key is a breaking change that a client sees and a unit test otherwise does not.
- **Framework canaries** in `pkg/grgcompat` (PROJECT_STRUCTURE.md).
- **Regression tests name the defect.** The doc comment gives the ID and the symptom, such as
  "BUG-142: an OPTIONS request ran the DELETE handler". Six months later, that comment is what
  stops someone deleting the test as redundant.

### Route contract tests

`controllers()` in `pkg/provider` is a plain function, so one unit-test file can check the
whole routing surface without booting anything. The template's
`pkg/provider/routes_test.go` has four tests:

| Test | Fails when |
|------|-----------|
| `TestRouteInventory` | `api/routes.txt` differs from the routes the server answers, or two routes share a name. The router keeps only the last route registered under a name |
| `TestHandlersAreResolvable` | A handler's parameters cannot be bound. It runs the router's own boot-time check, `grghttp.ValidateHandlerParameters` |
| `TestAPIRoutesRequireSignIn` | An `api` route has no `AuthMiddleware` and is not on the explicit `publicAPI` list, or `publicAPI` lists a route nobody serves |
| `TestAPIRoutesMatchTheSpec` | A route is served but not in `api/openapi.yaml`, or documented but not served |

```go
func TestAPIRoutesRequireSignIn(t *testing.T) {
	served := map[string]bool{}
	for _, route := range routes() {
		if route.GetNamespace() != "api" {
			continue
		}
		served[operation(route)] = true
		if !slices.Contains(publicAPI, operation(route)) && !slices.ContainsFunc(route.GetMiddlewares(), isAuth) {
			t.Errorf("%s has no AuthMiddleware; add one or list it in publicAPI", operation(route))
		}
	}
	for _, op := range publicAPI {
		if !served[op] {
			t.Errorf("publicAPI lists %q, which is not served: delete the line", op)
		}
	}
}
```

`api/routes.txt` turns every route change into a diff in review. It includes `GET /csrf`,
which `RouteProvider` mounts itself unless `DisableCsrfController()` is called:

```
GET /api/v1/notes                api.note.index
GET /api/v1/notes/{id}           api.note.show
GET /csrf                        gorgany.csrf.token   *middleware.RateLimitMiddleware
GET /healthz                     health.live
GET /readyz                      health.ready
POST /api/v1/notes               api.note.store       *middleware.AuthMiddleware
```

Regenerate it with `go test ./pkg/provider -run TestRouteInventory -update`, then review the
diff. A new public route, or a route that has lost its middleware, shows up in the same diff.
The inventory lists per-route middleware only. Filters such as the CSRF check apply by
pattern; `route_provider.go` lists them.

### Repository invariants: `test/arch`

| Test | Fails when |
|------|-----------|
| `TestLayering` | A package imports across the rules in PROJECT_STRUCTURE.md, or `known_violations.txt` lists an edge that has gone |
| `TestToolchainPin` | Either Dockerfile's `ARG GO_VERSION` (the app's or `test/e2e/runner.Dockerfile`), or a `golang:` image in `.gitlab-ci.yml`, differs from go.mod's `toolchain` |
| `TestEnvSampleIsComplete` | `config/config.yml` reads a `${VAR}` that `.env.sample` does not list |

These are ordinary tests that use only the standard library, so `go test ./...` enforces them
with no linter configured.

## The integration tier

```go
//go:build integration

package service

// TestMain lives in the tagged file only, so the untagged unit run is unaffected.
func TestMain(m *testing.M) {
	testsupport.AddMigration(migration.All()...)
	testsupport.Main(m)
}

func newNoteService(t *testing.T) *NoteService {
	t.Helper()
	// MustDatabase, not RequireDatabase: the integration tag is the opt-in, so a
	// missing engine here is a broken setup, not a reason to skip.
	database := testsupport.MustDatabase(t)

	databases := &db.DBContext{}
	databases.RegisterDataSource(core.DefaultKeyInRegistrar, database.DataSource())
	return &NoteService{DBContext: databases}
}
```

- **Run against a fresh, empty database.** `testsupport` truncates only the tables its own
  migration pass created in this process. Against a schema that already exists, idempotent
  `CREATE TABLE IF NOT EXISTS` migrations create nothing. Nothing is recorded, and isolation
  silently turns off.
  - Locally: the template's `compose.yaml` runs `postgres-test` on `tmpfs` at testsupport's
    default address, and `make test-integration` recreates it on every run.
  - In CI: use a service container per job. `make test-integration-ci` runs the suite without
    the recreate.
- **Run packages one at a time, `-p 1`.** Test binaries for different packages run in parallel
  by default, and each one truncates the shared database.
- **Use `GORGANY_TEST_*` for connection settings, and nothing else.** A variable per test means a
  variable per test to forget.
- **Build services the way the container would**, with a `db.DBContext` that holds the harness's
  datasource. `testsupport` boots no providers and does not set the package-level `db`
  globals. Code that reaches the database through `db.Connection()` is covered by the e2e tier
  instead.
- **For a migration that transforms data**, create the narrow table it expects, insert the rows,
  run that migration's `Up()`, and assert on the result.
- **The whole chain runs on every integration run.** The harness applies `migration.All()` once,
  in the first test that asks for a database, so a broken migration fails the run.
  `cli db:migrate up` also applies the framework's two sessions migrations first; add them to
  `AddMigration` when a test needs the `sessions` table (PROJECT_STRUCTURE.md).

## The e2e tier

The harness is the framework's own `e2e/` harness, with the changes an application needs:

- **It runs the production image, not `go run`.** Packaging defects then surface here instead
  of in production: a binary renamed in the Dockerfile but not in CI, or a directory the image
  does not contain.
- **It has one database engine**, at the production major version.

Both harnesses give every run its own compose project, `<name>-e2e-$$` unless
`COMPOSE_PROJECT_NAME` is set, so runs in parallel, in CI jobs or in git worktrees, cannot
collide.

```
test/e2e/
├── compose.yaml                   db, migrate, seed, app, runner
├── run.sh                         orchestration; the exit status is the suite's
├── .env.e2e                       tracked, obviously fake values; also the stack's database identity
├── runner.Dockerfile              the suite baked into an image
├── runner.Dockerfile.dockerignore its own allowlist: go.mod, go.sum, test/e2e/
├── main_test.go                   TestMain, live(), and the zero-executed guard
├── client_test.go                 call(), the envelope decoder, browser (a CSRF session), a database handle
├── cli_state_test.go              what migrate and seed left in the database
├── platform_test.go               health, 404 envelope, security headers: true of every app
├── security_test.go               CSRF, same origin, sign-in
└── notes_test.go                  one file per user journey
```

`run.sh` builds the image, unless CI passes `APP_IMAGE`, and then the runner. Then it runs
these steps:

1. `up -d --wait db`
2. `run --rm migrate`
3. `run --rm migrate` **again**. A second run must be a no-op, which proves the bookkeeping
   recognises what it applied.
4. `run --rm seed`
5. `up -d --wait app`. `--wait` returns when the image's own HEALTHCHECK passes.
6. `run --rm runner`: `go test -tags=e2e ./test/e2e/...` inside the compose network, with
   `E2E_BASE_URL=http://app:8080`.
   - The runner is an image built from `runner.Dockerfile`, not a `golang` container with the
     checkout bind-mounted. Under docker-in-docker, the usual CI setup, a bind mount resolves
     on the daemon's filesystem and comes up empty.
   - The runner needs its own `runner.Dockerfile.dockerignore`, because the application
     image's allowlist excludes every `_test.go` file.

**Copy the exit trap exactly.**

- It reads `$?` on its first line and exits with it on its last, so the teardown's `|| true`
  can never turn a failure into a green build.
- It dumps `docker compose ps` and the last 200 log lines **before** `down -v` deletes the
  containers that hold them.
- It removes the runner image the run built (`--rmi local`).
- It traps `INT` and `TERM` as well. On Debian and Ubuntu `/bin/sh` is dash, which skips an
  `EXIT` trap when a signal kills the script.

`main_test.go` has two guards, and both matter:

```go
// reached counts the tests that got past live(). With E2E_REQUIRE_LIVE=1 a run in
// which nothing reached the stack is a failure, not a green build: it means a
// wrong -run pattern, a missing build tag, or an unset E2E_BASE_URL.
var reached atomic.Int32

func TestMain(m *testing.M) {
	code := m.Run()
	if os.Getenv("E2E_REQUIRE_LIVE") == "1" && reached.Load() == 0 {
		fmt.Fprintln(os.Stderr, "e2e: E2E_REQUIRE_LIVE=1 but no test reached the application")
		code = 1
	}
	os.Exit(code)
}
```

`live(t)` skips when `E2E_BASE_URL` is unset, so `go test -tags=e2e ./...` on a laptop is
harmless. Under `E2E_REQUIRE_LIVE=1`, which the compose runner sets, that skip becomes a
failure. It waits up to 45 seconds for `/readyz` before it counts the test as reached.

**A browser session, by hand.** A mutating API request needs the session cookie and the token
from `GET /csrf` ([CSRF.md](CSRF.md)). `newBrowser(t, base)` fetches both, and `b.header()`
sends them. The cookie travels in a `Cookie` header rather than a cookie jar: it is a
`__Host-` cookie, which a jar will not send over plain HTTP. When several `Set-Cookie`
headers arrive, the last one wins, as it does in a browser after a session rotation.

**Rules for the suite.**

- **Black box.** `test/e2e` imports nothing from `pkg/`. It knows the app through HTTP, and
  through `E2E_DB_DSN` for assertions the API cannot make.
- **Each test creates its own, uniquely named data** and never asserts a global count. A count
  that depends on test order is a flake waiting for its first parallel run.
- **No test-only door into the production binary.** No test auth strategy, header or route.
  Sign in the way a client does. For an external identity provider, point the app at a mock
  issuer container in `compose.yaml`.
- **Stub external systems with containers, not code**: an SMTP sink with an HTTP API for mail,
  an S3-compatible store for uploads.
- **Run with `MODE=prod`** (`.env.e2e`), so the suite sees what production sends.
- **Log every request.** `call()` logs method, URL, status and a truncated body, so a CI failure
  can be diagnosed from the log alone.

Running it:

| Where | Command |
|-------|---------|
| Locally | `make e2e` |
| CI | `APP_IMAGE=$IMAGE COMPOSE_PROJECT_NAME=e2e-$CI_JOB_ID test/e2e/run.sh`. The test output, and on failure the containers' logs, are in the job log |

## Test data

| Kind | Where | Rule |
|------|-------|------|
| Reference data | `db/seeder`, in `All()` | Idempotent; safe in production; embedded with `//go:embed` |
| Development and e2e fixtures | `db/seeder/fixture` | Registered only when a config flag says so (PROJECT_STRUCTURE.md). Synthetic identities only |
| Unit and integration data | in the test, or in `test/testkit` | Builders, for example `actor(id, role)` |
| Golden files | `<package>/testdata/*.golden.json` | Rewritten with `-update`; the diff is reviewed |
| Large datasets | an external artefact | Imported by a console command, never tracked in git |

No real person's data goes into a fixture, a seeder or a golden file. A copied production row
stays in the repository's history after it is deleted from the working tree.

## Security regression checklist

Every application carries these. The template has no roles or uploads, so it shows only the
checks it can make, in `security_test.go` and `platform_test.go`.

- **In the unit tier:**
  - route-guard coverage (`TestAPIRoutesRequireSignIn`);
  - authorisation matrices;
  - injection payloads for sort, filter and search;
  - path-traversal payloads for file routes;
  - fail-closed boot checks for every security setting, such as an empty CORS allowlist or a
    short JWT secret.
- **In the e2e tier:**
  - 403 for a mutating request with no CSRF token;
  - 403 for a mutating request from another origin;
  - 401 without a signed-in user, and 403 for the wrong role;
  - 403 or 404 for another tenant's or owner's record;
  - security headers present;
  - 400 for a body over `http.security.body.maxRequestBytes`: a parse error, never a 500 and
    never a hang. The framework answers 400 here, not 413;
  - no internal error text in a 500 under `MODE=prod`. This holds only for errors routed to the
    default handler. A handler that panics with a string puts the string in the response in
    every mode.

## In CI

`verify`, `unit`, `integration` and `e2e` all run before `deploy` becomes available.
DEPLOYMENT.md has the pipeline. Two details belong here:

- **Wherever test output is piped** (`go test … | tee log`), the shell needs `set -o pipefail`.
  Without it, the job reports `tee`'s success and the failing suite is invisible.
- **Integration tests fail, rather than skip, in CI.** They use `MustDatabase`. A CI job that
  shows skips has lost its database.
