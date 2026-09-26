# Configuration

`config/config.yml` is the only config file. Every value that differs between
environments is a whole-value `${VAR}` placeholder resolved from the environment. An
unresolved placeholder becomes an empty string, so a variable missing in production
fails quietly unless something checks it.

`test/arch` fails the build when `config/config.yml` reads a variable that
`.env.sample` does not list. Variables the framework reads without a placeholder
(`MODE` here) are listed by hand. Keep this table in step with both.

## Environment variables

| Variable | Read by | Default | Required in prod | Secret |
|----------|---------|---------|------------------|--------|
| `MODE` | framework | `dev` | yes: `prod` (the image sets it) | no |
| `SERVER_PORT` | `app.server.port`; `app healthcheck` | – | yes | no |
| `APP_URL` | `app.server.url`; the same-origin check | – | yes: the boot panics without it | no |
| `DB_HOST` | `databases.default.host` | – | yes | no |
| `DB_PORT` | `databases.default.port` | – | yes | no |
| `DB_USER` | `databases.default.username` | – | yes | no |
| `DB_PASSWORD` | `databases.default.password` | – | yes | **yes** |
| `DB_NAME` | `databases.default.db` | – | yes | no |
| `DB_SSL` | `databases.default.ssl` | – | yes: `require` or stricter | no |
| `TZ` | the Go runtime, for `time.Local` | UTC in the image | recommended | no |

- `MODE` defaults to `dev` when unset, and dev mode puts the error and a stack trace in
  500 responses from the default error handler. The image sets `MODE=prod`; do not
  override it in production.
- Set `TZ` in the container's environment: the image's `.env` is empty. In development a
  `TZ` line in `.env` also works, because `app.Run` loads `.env` before anything reads
  local time, unless the app's own `init` or `main` reads it first. `app.server.timezone`
  is read but never applied.
- `DB_SSL=verify-full` checks the database's certificate against the system CA bundle,
  in the app and in the `backup` service alike. `verify-ca` against a private CA needs
  the CA file mounted into both, with `PGSSLROOTCERT` pointing at it for the backup.
- `APP_URL` is the app's own origin, which the same-origin check compares against. An
  empty value panics the boot.

## Keys with fixed values

| Key | Value | Why |
|-----|-------|-----|
| `auth.session.storage` | `database` | Sessions survive restarts and are shared between replicas |
| `databases.default.driver` | `postgres_gorm` | Needs the driver's blank import in `pkg/provider/bootstrap.go` |
| `app.gorgany.validate` | `false` | The app does not use the generator; there is no lock file to check |
