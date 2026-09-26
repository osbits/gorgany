# Building, configuring and deploying an application

Most deployment defects in applications built on the framework are disagreements between files
that each look fine on their own:

- **Names and paths disagree.** The Dockerfile names the binaries one way and CI invokes them
  another, so the migrate job runs a path the image does not contain.
- **The entry point swallows commands.** An `ENTRYPOINT` turns
  `docker compose run app /app/cli db:migrate up` into "start the server".
- **The build context leaks.** A denylist `.dockerignore` that misses something sends `.git`,
  `.env` or key files to the build daemon.
- **Local runs reach production.** A repository-root `.env` with production values makes every
  `go run` from a checkout talk to production, because the framework loads `./.env`
  unconditionally.
- **Deploys happen out of order.** A pipeline that deploys before it migrates lets new code meet
  the old schema.

This document describes the supporting files that keep those in agreement, and why each line
is there. Every file shown here is in [app-template/](app-template/). The template's e2e suite
runs against the image this Dockerfile builds.

## The image

```dockerfile
# syntax=docker/dockerfile:1
# GO_VERSION must equal go.mod's toolchain directive; test/arch enforces it.
ARG GO_VERSION=1.26.5

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
# GOTOOLCHAIN=local: never download a second toolchain in the middle of a build.
ENV GOTOOLCHAIN=local CGO_ENABLED=0
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Package builds, not file builds: each directory name becomes a binary name.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X $(go list -m)/pkg/buildinfo.Version=${VERSION}" \
      -o /out/ ./cmd/app ./cmd/cli
# The runtime filesystem: a non-root user, a sticky /tmp, and an empty .env, because
# the framework refuses to start without one. Real values arrive as environment
# variables, which the empty file never overrides.
RUN mkdir -p /rootfs/etc /rootfs/tmp /rootfs/app \
 && chmod 1777 /rootfs/tmp \
 && touch /rootfs/app/.env \
 && printf 'app:x:65532:65532::/app:/sbin/nologin\n' > /rootfs/etc/passwd \
 && printf 'app:x:65532:\n' > /rootfs/etc/group \
 && mkdir -p /writable/resource/temp /writable/public/storage

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /rootfs/ /
WORKDIR /app
COPY --from=build /out/app /out/cli /app/
COPY --from=build /src/config/ /app/config/
COPY --from=build /src/resource/ /app/resource/
# The only directories the app user may write: resource/temp (upload temp copies)
# and resource/public/storage. The framework stores File.Write(p, …) under
# resource/public/<p>, so every p starts with "storage/"; any other p fails with
# "permission denied" here. Mount the production volume on resource/public/storage;
# an empty named volume inherits this owner.
COPY --from=build --chown=65532:65532 /writable/resource/ /app/resource/
COPY --from=build --chown=65532:65532 /writable/public/ /app/resource/public/
USER 65532:65532
ENV MODE=prod
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s CMD ["/app/app", "healthcheck"]
# CMD, not ENTRYPOINT: `docker compose run --rm app /app/cli db:migrate up` must
# replace the command, not pass its arguments to the server.
CMD ["/app/app"]
```

| Line | Why |
|------|-----|
| `ARG GO_VERSION` equal to go.mod's `toolchain` | A floating `golang:1.26` tag changes the compiler whenever the tag moves. The build never downloads a toolchain (`GOTOOLCHAIN=local`, which the official image also sets), so an image older than go.mod's `toolchain` compiles with its own older compiler and says nothing. Pinning the tag keeps the two equal, and `TestToolchainPin` fails when the Dockerfiles, the CI image and go.mod disagree |
| `-o /out/ ./cmd/app ./cmd/cli` | Package builds name each binary after its directory. File builds (`go build cmd/app.go`) record `command-line-arguments` as the main package, and they let two `main` packages share a directory |
| `WORKDIR /app`, with `config/` and `resource/` beside the binaries | The framework resolves both against the working directory |
| The empty `/app/.env` | `app.Run()` panics when `./.env` is missing. godotenv never overrides a variable that is already set, so the orchestrator's environment wins over the file |
| `scratch`, `USER 65532` | No shell, no package manager, no root. Zone data comes from `time/tzdata` in the binaries; CA certificates are copied in |
| The two `--chown` copies | Only `resource/temp` (upload temp copies) and `resource/public/storage` are writable. The framework writes a stored upload to `resource/public/<p>`, where `p` is the path the app passes to `File.Write`, so every `p` starts with `storage/`. An empty named volume mounted there takes this owner, so the non-root process can write to it |
| `ENV MODE=prod` | The framework reads `MODE` from the environment and defaults to `dev`. Dev mode puts the error and a stack trace in every 500 that the default error handler produces. The image fails closed; a developer running it locally sets `MODE=dev` on purpose |
| `HEALTHCHECK … /app/app healthcheck` | `scratch` has no `curl`. `app healthcheck` sends one GET to `/readyz` on the loopback interface and boots nothing |
| `CMD`, not `ENTRYPOINT` | So a one-off `run --rm app /app/cli db:migrate up` runs the console. With `ENTRYPOINT ["/app/app"]`, those arguments go to the server, which ignores them and starts serving, and the "migration job" never exits |

## Ignore files

`.dockerignore` is an **allowlist**:

```
*
!go.mod
!go.sum
!cmd/
!pkg/
!db/
!config/
!resource/
resource/temp/
resource/public/storage/
**/*_test.go
**/testdata/
```

Anything not listed never reaches the build daemon: `.git`, `.env`, private keys, local
binaries, IDE state. A denylist has to anticipate every one of those; an allowlist has to
anticipate nothing. The e2e runner image has its own allowlist,
`test/e2e/runner.Dockerfile.dockerignore`, next to its Dockerfile.

`.gitignore` anchors build-output patterns to the root:

```
# Build output. Anchored to the root: an unanchored `app` or `cli` would also
# ignore the cmd/app and cmd/cli source directories.
/bin/
/app
/cli
coverage.*
*.test

# Local environment and key material
.env
.env.*
!.env.sample
!/test/e2e/.env.e2e
*.pem
*.key
id_rsa
id_ecdsa
id_ed25519

# Runtime state
/logs/
/tmp/
resource/temp/
resource/public/storage/

# Fixes land as commits, never as patch files
*.patch

# Editors and OS
.idea/
.vscode/
.DS_Store
```

- **An unanchored `app` or `cli` pattern in `.gitignore` also ignores `cmd/app` and `cmd/cli`**,
  the source directories. `.dockerignore` patterns are relative to the context root, so there
  the danger is an explicit `cmd/app` entry, or a `**/app`.
- **`.env.*` also matches `test/e2e/.env.e2e`.** The template un-ignores that file explicitly:
  its values are fake by construction, and the harness needs it tracked.
- **Key material never lives inside a working tree, ignored or not.** An ignored file is one
  `git add -f`, or one careless build context, away from being published. Keep identity files
  and deploy keys in `~/.ssh` or the CI secret store. A runbook that generates them writes
  them outside the repository, with mode `0600`.
- **Fixes land as commits, never as tracked `.patch` files.**

## Configuration

- **One file.** `config/config.yml` is the only configuration the framework reads, and there are
  no per-environment overlays. Every value that differs between environments is a `${VAR}`
  placeholder.
- **A placeholder must be the whole value.** `url: ${APP_URL}` works. `url: https://${HOST}` and
  `${VAR:-default}` do not.
- **An unresolved placeholder becomes an empty string**, with a warning in the log. These keys
  stop the boot instead: `auth.jwt.secret`, `auth.session.cookie.secure`, and every
  datasource's `databases.<name>.auth.client_secret` and
  `databases.<name>.auth.certificate_password`. Every other key, `app.server.url`,
  `databases.<name>.password` and `databases.default.ssl` included, boots with the empty value,
  so the provider that reads a security setting checks it itself. The template's route
  provider panics on an empty `app.server.url`. Up to and including v2.4.3, a datasource has
  no `auth` block, so only `auth.jwt.secret` and `auth.session.cookie.secure` stop the boot.
- **The framework reads some variables that no placeholder of yours mentions.** It reads `MODE`,
  and the mail service's `SMTP_*`, straight from the environment. `JWT_SECRET` is the default
  for `auth.jwt.secret` as soon as any `auth.jwt` key is set, and it must be at least 32 bytes.
  List these in `.env.sample` and `docs/configuration.md` by hand: the placeholder check cannot
  see them.
- **`.env.sample` lists every variable**, grouped, with one comment line each: what it is,
  whether production needs it, and whether it is secret. `TestEnvSampleIsComplete` fails when
  `config.yml` reads a variable the sample does not list. Values in the sample are obviously
  fake (`change-me`).
- **`.env` is for development only.** Production values live in the host's env file or in the CI
  secret store, and never in a working copy. The framework loads `./.env` from whichever
  directory it starts in. Never start a production env file from `.env.sample`: its `MODE=dev`
  would override the image's `MODE=prod`.
- **Configuration is read in `pkg/provider`**, in provider constructors behind `deferred`, or in
  `Register`/`Boot` (PROJECT_STRUCTURE.md), and handed to services as typed values. The one
  exception is `health.Probe`, which reads `SERVER_PORT` before any boot. Make an empty
  security setting panic the boot.
- **Set `TZ` in the container's environment, or use explicit locations.** `app.server.timezone`
  is read but never applied to `time.Local`, and an invalid zone name is ignored silently. The
  image's `.env` is empty, so `TZ` comes from the orchestrator. In development a `TZ` line in
  `.env` works too, because `app.Run` loads `.env` before anything reads local time.

## One source of commands: the Makefile

CI's verify, lint-api, unit, integration and vuln jobs run `make` targets, and people run the
same targets; `make help` lists them. The build and e2e jobs run `docker build` and
`test/e2e/run.sh` directly, the same commands that `make docker` and `make e2e` wrap.

| Target | Runs |
|--------|------|
| `make build` | `go build -trimpath -ldflags … -o bin/ ./cmd/app ./cmd/cli` |
| `make run`, `make cli ARGS=…`, `make migrate`, `make seed` | The two binaries through `go run`, against `.env` |
| `make dev-up` | `docker compose up -d --wait postgres`: the local database, on the port `.env` names |
| `make verify` | `gofmt -l .`, `go vet ./...`, `go vet -tags=integration,e2e ./...`, `go mod tidy -diff` |
| `make lint`, `make lint-api` | golangci-lint and the redocly spec linter, both at pinned versions |
| `make test` | `go test -race -count=1 ./...` |
| `make test-integration` | Recreates the `tmpfs` test database, runs `make test-integration-ci` (`go test -tags=integration -p 1 ./...`), then removes the database so port 5433 is free for the next service |
| `make e2e` | `test/e2e/run.sh` |
| `make vuln` | `govulncheck ./...`, at a pinned version |
| `make docker` | Builds the production image as `$(IMAGE_REPO):$(VERSION)`. `VERSION` is the tag on a tagged commit, otherwise the 8-character SHA, as in CI |

The Makefile sets `SHELL := bash` and `.SHELLFLAGS := -eu -o pipefail -c`, so a recipe that pipes
test output into `tee` fails when the tests do.

## The pipeline

```
verify, lint-api ─► unit, integration, vuln, secret detection
                 ─► build (push $IMAGE, which is registry:<SHA>-<pipeline number>)
                 ─► e2e and container scanning, against that image
                 ─► deploy (manual, protected default branch or v* tag only)
```

- **One image per pipeline, never overwritten.** The tag is the commit SHA plus the pipeline
  number, so a tag pipeline, a branch pipeline and a scheduled rebuild of the same commit
  never replace each other's image. The e2e job tests that image, and the deploy job ships it.
  Never deploy `:latest`, and never rebuild between testing and shipping. Give the registry
  a cleanup policy for old images.
- **Deploy is manual and runs only on protected refs**: the default branch and `v*` tags.
  - Protect the `v*` tags (Maintainers only) as well as the branch: GitLab passes protected
    variables only to pipelines on protected refs.
  - `DEPLOY_HOST`, `SSH_KNOWN_HOSTS` and `SSH_PRIVATE_KEY` are protected variables.
  - GitLab cannot mask a value that spans lines or contains spaces. So store the key
    base64-encoded (`base64 -w0`) in a Variable, not a File, and let the job decode it.
  - `resource_group: production` keeps two deploys from migrating the same database at once.
- **Run deploy on a runner of its own.** A privileged runner gives every job on it root on the
  runner host, a merge request's jobs included. Register a separate, unprivileged runner,
  mark it protected so it runs only jobs from protected refs, and tag it `deploy`. Keep the
  privileged runner for the build and e2e jobs.
- **Pin SSH host keys** (`known_hosts` from a CI variable), and never pass
  `StrictHostKeyChecking=no`.
  - Deploy as a dedicated user, not a person's account. Membership of the `docker` group is
    root-equivalent on the host, so treat the deploy key as a root credential for that host:
    use it for nothing else, and rotate it when a maintainer leaves.
  - To narrow it, install a deploy script on the host and pin the key to it with
    `restrict,command="…"` in `authorized_keys`.
- **Scan for secrets and vulnerabilities.**
  - The template includes GitLab's secret-detection and container-scanning templates, and
    runs container scanning in tag pipelines too.
  - Both GitLab jobs only report findings (`allow_failure`), so they never stop a deploy. To
    gate on findings, add a job that fails, such as
    `trivy image --exit-code 1 --severity HIGH,CRITICAL $IMAGE`.
  - `govulncheck` runs in every pipeline, and in the daily pipeline schedule you create in
    GitLab's settings.
- **Docker-in-docker needs a privileged runner** whose `config.toml` shares `/certs/client`. The
  template's `.docker` job sets `DOCKER_HOST` and the TLS variables explicitly rather than
  relying on the image to guess them.
- **Protected environments and required code-owner approval are GitLab Premium features.** On
  GitLab Free, protected refs and Maintainer-only merges are the gates.

The template's `.gitlab-ci.yml` is this pipeline, except for `oasdiff`. Add that when the API
has a first release to compare against. The same stages map one-to-one onto GitHub Actions
jobs.

## Releasing

**Once per host:**
- Create the deploy user.
- Write `/etc/myapp/app.env`, mode `0600`, owned by the deploy user, with every variable from
  `docs/configuration.md`, including `MODE=prod` and `DB_SSL=require` or stricter.
- Log the deploy user in to the registry with a read-only deploy token.
- Create `/var/backups/myapp`, mode `0700`, owned by the deploy user, and arrange for its
  contents to be copied off the host after every release. A dump holds every row, password
  hashes and sessions included.

The template's runbook, `docs/runbooks/deploy.md`, has the commands.

**Every release.** CI's `deploy` job performs steps 1 to 5; step 6 is yours. By hand, run the
same commands on the host from `/srv/myapp`, after `export IMAGE=<the tag>`:
`compose.prod.yml` refuses every command without it.

1. Copy `deploy/compose.prod.yml` to `/srv/myapp/` and pull the image CI built and tested.
2. **Back up the database**: `docker compose -f compose.prod.yml run --rm backup`. This is the only
   way back from a migration that drops or rewrites data.
3. **Migrate with the new image while the old version keeps serving**:
   `docker compose -f compose.prod.yml run --rm migrate`. A failing migration exits `1`, and the
   release stops there.
4. **Seed reference data**: `docker compose -f compose.prod.yml run --rm seed`. Each seeder runs
   once per `Name()`, so the step is a no-op when nothing is new. A failing seeder exits `1`
   with none of its rows saved, and the release stops there.
5. **Roll the app**: `docker compose -f compose.prod.yml up -d --wait app`. `--wait` returns when
   the image's HEALTHCHECK passes, which means `/readyz` answered.
6. Check that `/healthz` reports the new version.

Because the old version serves while step 3 runs, **every migration must work with the previous
release's code**. Add columns and tables in one release, and remove what is no longer used in a
later one. A migration that drops or rewrites data cannot be undone by `db:migrate down`.
Rolling back means restoring the backup from step 2, and the release's changelog deployment
notes say so.

`deploy/compose.prod.yml` puts the one-off services behind the `ops` profile, so `up` never
starts them by accident:

```yaml
x-app: &app
  image: ${IMAGE:?set IMAGE to the tag being deployed}
  env_file: /etc/myapp/app.env
  restart: unless-stopped

services:
  app:
    <<: *app
    ports: ["127.0.0.1:8080:8080"]
    volumes: [uploads:/app/resource/public/storage]
    stop_grace_period: 40s             # longer than app.server.timeout.shutdown

  migrate:
    <<: *app
    command: ["/app/cli", "db:migrate", "up"]
    restart: "no"
    profiles: [ops]

  seed:
    <<: *app
    command: ["/app/cli", "db:seed"]
    restart: "no"
    profiles: [ops]

  backup:
    image: postgres:17-alpine          # the server's major version
    env_file: /etc/myapp/app.env
    volumes: ["/var/backups/myapp:/backups"]
    entrypoint: ["sh", "-c"]
    command:
      - >-
        case "$$DB_SSL" in verify-full) export PGSSLROOTCERT=system;; esac;
        PGPASSWORD="$$DB_PASSWORD" PGSSLMODE="$$DB_SSL"
        pg_dump -Fc -h "$$DB_HOST" -p "$$DB_PORT" -U "$$DB_USER" -d "$$DB_NAME"
        -f "/backups/$$(date -u +%Y%m%dT%H%M%SZ).dump"
    restart: "no"
    profiles: [ops]

volumes:
  uploads: {}
```

`pg_dump` needs `PGSSLROOTCERT=system` to verify a server against the image's CA bundle under
`DB_SSL=verify-full`. It refuses that setting for weaker modes, so the backup service sets it
only for `verify-full`.

Production seeding runs only the reference seeders in `db/seeder.All()`. Fixtures are
registered only when their config flag is set, and production never sets it.

**Version the application with tags.** To cut a release:
1. Move the `[Unreleased]` entries of `CHANGELOG.md` under `vX.Y.Z` and merge.
2. Push the protected tag `vX.Y.Z`.

The tag's pipeline builds and tests that image, and offers the manual deploy.
- CI stamps `VERSION=${CI_COMMIT_TAG:-$CI_COMMIT_SHORT_SHA}`. The Makefile stamps the same:
  the tag on a tagged commit, otherwise the 8-character SHA.
- `/healthz` reports it, and so does `cli version`. The template's own `version` command
  replaces the framework's, which prints the framework's version.

## More than one datasource

`db:migrate up` and `db:seed` operate on one datasource: `default`, unless
`--datasource=<name>` says otherwise. Each skips the migrations or seeders that target another
datasource, and logs each at info level. Put the flag after `up` or `down`:
`db:migrate --datasource=<name> up` exits 2. `db:diff --datasource=<name>` diffs one datasource,
and refuses a MySQL one (PROJECT_STRUCTURE.md, "Migrations and seeders").

Up to and including v2.3.2, `db:seed` cannot select a datasource. `cli db:seed
--datasource=<name>` exits 2 with `flag provided but not defined: -datasource`, because the
console's flag parser rejects a flag the command does not declare. On those versions `db:seed`
seeds `default` only, so keep seeders there.

- A migration or seeder for a second database implements `DataSourceName() string` and stays in
  the same `All()`.
- Add one `migrate-<name>` service per extra datasource to `compose.prod.yml`, with
  `command: ["/app/cli", "db:migrate", "up", "--datasource=<name>"]` and the `ops` profile, and
  run each in the migrate step. If that datasource has seeders, add a `seed-<name>` service the
  same way, with `command: ["/app/cli", "db:seed", "--datasource=<name>"]`, and run it in the
  seed step.
- Back up each database.
- Make `/readyz` ping every datasource the application cannot serve without.

## More than one instance

- **Every server replica runs every job.** `JobProvider.Boot` starts the scheduler in each one,
  and nothing elects a leader. Make each job idempotent. At the start of `Run`, take a
  Postgres advisory lock (`SELECT pg_try_advisory_lock(<job id>)`), and return if it is held
  elsewhere.
- **Sessions must live in the database.** `auth.session.storage` defaults to `memory`, which one
  replica cannot see from another. Use `database`, as the template does.
- **Rate limits are per instance** ([RATE_LIMITING.md](RATE_LIMITING.md)).
- **Uploads need shared storage.** The `uploads` volume in `compose.prod.yml` exists on one host
  only.

## Upgrading the framework and dependencies

- **Upgrade gorgany in a merge request of its own.** First read the framework's `CHANGELOG.md`,
  and for a major version `MIGRATION_v2.md`. Then run
  `go get github.com/osbits/gorgany/v2@vX.Y.Z && go mod tidy` and all four tiers, e2e included.
  If a canary in `pkg/grgcompat` fails, a defect you work around has been fixed: delete the
  workaround and its canary in the same merge request.
- **Bump the toolchain in four places at once**: go.mod's `toolchain`, `ARG GO_VERSION` in
  `Dockerfile` and `test/e2e/runner.Dockerfile`, and the CI image. `TestToolchainPin` checks
  them.
- **Let a bot open the other updates** (Renovate or Dependabot), grouped weekly, covering Go
  modules, base images and CI images. Merge them only through the full pipeline. The scheduled
  `govulncheck` covers what arrives between updates.

## Health and observability

| Endpoint | Answers 200 when | Used by |
|----------|------------------|---------|
| `/healthz` | The process serves HTTP. It touches no dependency, and it reports the build version | Liveness probes: restart only a process that has stopped answering |
| `/readyz` | The default datasource answers a ping within a second | Readiness probes, `app healthcheck`, `up --wait`, load balancers |

Both are excluded from the session filter. A probe every few seconds must not create a session
row each time.

- **Logs go to the process's standard streams**, and the platform collects them. The framework's
  `DefaultLogger` writes plain-text lines to **stderr** through the standard `log` package, so
  collect both streams. A log file inside a container disappears with the container.
- **Structured logs.** For JSON, rebind `core.Logger` in `AppProvider.Register`, with an adapter in
  `pkg/adapter/<sink>`, for example over `log/slog`'s `JSONHandler`. Never log a request body, a
  token, a cookie or a password; log IDs.
- **Metrics.** The framework exports none. Add a filter in `pkg/middleware` that records the
  route name (never the raw path, which is unbounded), the method, the status and the duration.
  Serve `/metrics` excluded from the session filter, like the probes, and block it at the
  reverse proxy.
- **The request ID.** The framework generates a fresh UUID per request, and ignores an inbound
  `X-Request-Id`. The ID is available as
  `message.Context().Value(core.MessageContextKey).(core.IMessageContext).GetRequestId()`, but
  the framework neither sends it back nor logs it. An application that wants correlation adds a
  filter that sets `X-Request-Id` on the response and puts the ID in every log line.
- **Shutdown drains, within `app.server.timeout.shutdown` (default `30s`).** On `SIGTERM` or
  `SIGINT` the server does four things in order:
  1. It stops accepting connections and waits for in-flight requests.
  2. It stops `JobProvider`'s scheduler, which cancels the context each running job was given,
     and waits for the jobs to return.
  3. It waits for the async subscribers of the bound `core.IEventBus`. Requests and jobs both
     start them.
  4. It closes `DbProvider`'s datasources.

  When all four finish in time, the process exits `0`. At the deadline it closes the
  connections still open and leaves the datasources open, logs what it stopped waiting for,
  and exits `1`. A second signal ends the process at once. Goroutines the application started
  itself are not waited for.
  - **The orchestrator's grace period must be longer than the timeout**, or it sends `SIGKILL`
    first. Compose and `docker stop` wait 10 seconds by default, so give the `app` service
    `stop_grace_period: 40s`. Kubernetes waits 30 seconds: raise
    `terminationGracePeriodSeconds`, or lower the timeout.
  - **The listener closes as soon as the signal arrives**, and new connections are refused. A
    load balancer that has not noticed yet still sends them. With several replicas, take the
    instance out of rotation first, then send the signal: drain it at the load balancer, or
    give Kubernetes a `preStop` delay.
  - **Hijacked connections are not waited for.** A handler that calls `Hijack`, a WebSocket for
    example, owns its connection, and the process exit ends it.

## `scripts/`

- **A script that changes data becomes a console command** in `pkg/command`, with a dry-run
  flag, running in a transaction, and registered in `command_provider.go`. So does a script
  that re-implements logic the server already has, such as a calculation or a data
  transformation a service owns.
- **What remains in `scripts/` is read-only, tracked and `shellcheck`-clean.**
- **No script defaults to a production URL.** The target is a required argument.

## Security checklist

- `MODE=prod` in the image, and nothing in production overrides it.
- The host's env file is mode `0600` and owned by the deploy user. No `.env` with production
  values exists in any working copy. No key material lives inside the repository tree, ignored
  or not.
- The backup directory is mode `0700` and owned by the deploy user, and its dumps are copied
  off the host.
- `.dockerignore` is an allowlist, and so is the e2e runner's. `.gitignore` covers `.env*`, keys
  and build output.
- Mutating routes that a session cookie authenticates sit behind the CSRF filter and the
  same-origin check.
- Seeders, fixtures and golden files never hold real personal data. Anything that ever did is
  purged from history, and any credentials it held are rotated.
- Anything secret that was ever committed is rotated. Deleting it from the tree does not remove
  it from history.
- The runtime image runs as a non-root user and has no shell.
- The pipeline runs secret detection, `govulncheck` and container scanning.
- SSH host keys are pinned. Deploys run as a dedicated deploy user, on a runner of their own,
  only from the protected default branch or a protected `v*` tag, one at a time.
