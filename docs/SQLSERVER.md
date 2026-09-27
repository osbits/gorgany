# SQL Server and Azure SQL

The `sqlserver_gorm` driver connects to SQL Server 2016 or newer and to Azure SQL Database. It
signs in with a SQL login, or with Microsoft Entra ID (formerly Azure Active Directory). This
document covers connecting, signing in, and SQL Server as the `default` gorgany owns. What the
dialect refuses and translates is in
[DIALECTS.md](DIALECTS.md#what-sql-server-refuses-and-translates), and how a second datasource
is deployed is in [DEPLOYMENT.md](DEPLOYMENT.md#more-than-one-datasource).

## Imports

Neither package is part of `driver/builtin`. The driver links go-mssqldb, and the Entra ID
methods also link the Azure identity SDK, MSAL and a browser opener. An app that does not use
them should not have to link them. Add the import in `pkg/provider/bootstrap.go`, next to the
app's other driver import:

```go
import (
    _ "github.com/osbits/gorgany/v2/db/sql/driver/postgres" // default

    // A SQL login only:
    _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
    // Or, to sign in with Entra ID. This registers the driver too:
    _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread"
)
```

Without the `azuread` import, an Entra ID `auth.method` fails the boot, and the error quotes the
import line. A third package, `azuread/persistentcache`, keeps a person's sign-in across restarts
on a development machine (see "Remembering the sign-in across restarts").

## Configuration

Write every value that differs between environments as a whole-value placeholder
([DEPLOYMENT.md](DEPLOYMENT.md#configuration)). This config reaches an externally owned Azure
SQL database under the name `legacy`:

```yaml
databases:
  default:                             # the database gorgany owns: migrations, sessions
    driver: postgres_gorm
    # …
  legacy:
    driver: sqlserver_gorm
    host: ${LEGACY_DB_HOST}            # example.database.windows.net
    port: 1433
    db: ${LEGACY_DB_NAME}              # Example-db; hyphens are fine
    username: ${LEGACY_DB_USER}        # interactive: only the account the sign-in suggests
    external_schema: true              # another system owns the schema
    read_only: true
    lazy_connect: true                 # see "A second datasource" below
    auth:
      # interactive | azure_cli (development); managed_identity | workload_identity (deployed)
      method: ${LEGACY_DB_AUTH_METHOD}
      tenant_id: ${LEGACY_DB_TENANT_ID}
    options:
      app_name: my-app
    properties:
      maxOpenConnections: 10
```

Do not name your own placeholders `AZURE_*`. azidentity reads those variables by itself (see
"Deployed apps").

### From DataGrip or SSMS

Copy the connection field by field. A URL, JDBC string or connection string is not accepted,
because a string that sets a key twice has no rule for which value wins.

| DataGrip / SSMS field | gorgany key | Notes |
|---|---|---|
| Host, Server name | `host` | The name alone. `tcp:`, `,1433`, `:1433` and `\instance` are refused, and the refusal names the key each part belongs in. |
| Port | `port` | `0` or unset means 1433. |
| Instance, or the `\instance` of `host\instance` | `instance` | JDBC's rule, which DataGrip and SSMS follow: when `port` is set, the instance is ignored, with a boot warning. Without a port, on premises, it is found through SQL Server Browser (UDP 1434). On an Azure host it is always dropped, with a warning. |
| Database | `db` | As written. |
| User | `username` | The SQL login, or the account an interactive sign-in suggests. |
| Password | `password` | SQL login only. It is refused with an Entra ID method. |
| Authentication: SQL Server, User & Password | `auth.method: sql`, or no `auth` block | |
| Authentication: Azure Active Directory / Microsoft Entra MFA, "Universal with MFA", interactive | `auth.method: interactive` | One browser prompt per datasource. |
| Authentication: Azure Active Directory / Microsoft Entra Service Principal | `auth.method: service_principal` | |
| Authentication: Managed Identity | `auth.method: managed_identity` | A user-assigned identity's client ID, resource ID or object ID goes in `auth.client_id`, `auth.resource_id` or `auth.object_id`. None selects the resource's default identity, usually its system-assigned one. |
| Authentication: Default | `auth.method: azure_default` | |
| Authentication: Entra Password, Integrated, Windows | none | Refused. A user's password cannot satisfy MFA or Conditional Access, and Windows or Kerberos sign-in is not supported. |
| Encrypt (Mandatory, Optional, Strict) | `ssl` | `true`, `false` or `strict`; see below. |
| Trust server certificate | `options.trust_server_certificate` | Refused on Azure hosts, with `ssl: strict`, and with an Entra ID method. |
| ApplicationIntent=ReadOnly | `read_only: true` | This sets the intent. `options.application_intent` overrides it; see "Azure notes". |
| Application name | `options.app_name` | The default is `gorgany`. |

### `ssl`

`ssl` maps to go-mssqldb's `encrypt`:

| `ssl` | `encrypt` | Meaning |
|---|---|---|
| unset, `""`, `true`, `yes`, `1`, `mandatory` | `true` | TLS, and the server certificate is verified. An unresolved placeholder reads as `""`, so it can never weaken the connection. |
| `strict` | `strict` | TDS 8.0: TLS before the pre-login. Azure SQL and SQL Server 2022 support it. |
| `false`, `optional`, `no`, `0` | `false` | Only the login is encrypted. |
| `disable` | `disable` | Nothing is encrypted. |

An Azure SQL host accepts only `true` or `strict`, and so does every Entra ID method, on any
host. An Entra ID method sends an access token in the login. `ssl: false` sends the login in
clear to a server, or anything in between, that says it cannot encrypt, and `disable` always
does. The token lets whoever holds it into every database the principal can reach, and a
private endpoint behind a DNS name of its own is still Azure SQL, so the host's name does not
decide.

`trustservercertificate=false` is always sent. For a local docker SQL Server with a self-signed
certificate and a SQL login, set `options.trust_server_certificate: true`. An Entra ID method
refuses that option too, because it would hand the token to any server that answers. Verify
such a server instead: `options.certificate` or `options.server_certificate` names its
certificate, and `options.hostname_in_certificate` names a certificate issued for another name
than `host`.

### `options`

`options` is a closed list. go-mssqldb ignores a key it does not know, so a misspelt key would
change nothing and say nothing. Each option takes the snake_case name or go-mssqldb's own key.

| Option | go-mssqldb key | Value |
|---|---|---|
| `app_name` | `app name` | a name; default `gorgany` |
| `application_intent` | `applicationintent` | `ReadOnly` or `ReadWrite` |
| `guid_conversion` | `guid conversion` | boolean; default `true` (see "Types") |
| `trust_server_certificate` | `trustservercertificate` | boolean; default `false` |
| `hostname_in_certificate`, `certificate`, `server_certificate`, `tls_min` | same, without `_` | TLS verification |
| `dial_timeout`, `connection_timeout`, `keep_alive` | `dial timeout`, `connection timeout`, `keepalive` | seconds; not defaulted |
| `packet_size`, `workstation_id`, `protocol`, `pipe`, `timezone`, `no_trace_id`, `disable_retry`, `log` | as go-mssqldb | as go-mssqldb |
| `failover_partner`, `failover_port`, `failover_partner_spn`, `multi_subnet_failover`, `server_spn`, `epa_enabled` | as go-mssqldb | as go-mssqldb |

A key that a typed key owns is refused, and the error names the typed key. For example,
`database` goes in `db`, `user id` in `username`, `server` in `host`, `encrypt` in `ssl`, and
`fedauth`, `authentication` and `applicationclientid` in `auth`. Pool settings that are left
unset default to 10 open connections and 5 idle ones. An idle connection is closed after 5
minutes, and every connection after 30.

## Signing in

`auth.method` selects the sign-in. Each method refuses any `auth` key it never uses.

| Method | Signs in as | Required | Optional | Where |
|---|---|---|---|---|
| `sql` (or no `auth`) | a SQL login | `username`, `password` | | anywhere |
| `interactive` | a person, in the system browser | | `tenant_id`, `client_id`, `redirect_url`, `token_cache`, `authentication_record_path`; `username` is the suggested account | development |
| `device_code` | a person, with a code the process prints | | `tenant_id`, `client_id`, `token_cache`, `authentication_record_path` | development |
| `azure_cli` | the account `az login` chose | | `tenant_id` | development |
| `azure_default` | whatever azidentity's DefaultAzureCredential finds | | `tenant_id`, for some of the credentials it tries (see "Deployed apps") | deployed |
| `service_principal` | an app registration | `tenant_id`, `client_id`, and `client_secret` or `certificate_path` | `certificate_password`, `send_certificate_chain` | deployed |
| `managed_identity` | the managed identity of the Azure resource the app runs on | | at most one of `client_id`, `resource_id` and `object_id`, for a user-assigned identity; none is the resource's default identity, usually its system-assigned one (see "Deployed apps") | deployed, on Azure |
| `workload_identity` | the app registration or user-assigned identity a Kubernetes service account is federated with | | `tenant_id`, `client_id`, `token_file_path`; each one left empty comes from the environment (see "Deployed apps") | deployed, on Kubernetes |

Every Entra ID method also takes two keys:

- `scope` overrides the token scope, which is otherwise derived from the host.
- `login_timeout`, in seconds, bounds one sign-in. The default is 300 for `interactive` and
  `device_code`, 120 for `managed_identity`, and 60 for the others. The instance metadata
  service of a VM, a scale set or an AKS node may answer 410 for up to about 70 seconds while
  it updates, and azidentity retries it for about 100 seconds, which 120 leaves room for.
  `azure_default`'s managed identity retries the same way, so where it signs in with one, give
  it `login_timeout: 120` too.

`password` is refused with any of these methods. A service principal's secret goes in
`auth.client_secret`. An unresolved placeholder there, or in `certificate_password`, stops the
boot. The certificate is a PEM file with an unencrypted key, which takes no
`certificate_password`, or a PKCS#12 (`.pfx`) file that `certificate_password` opens. azidentity
reads only the legacy PKCS#12 profile, a SHA-1 MAC over 3DES or RC2. OpenSSL 3 and current
Windows export AES and SHA-256 by default, and such a file is refused; re-export it with
`openssl pkcs12 -export -legacy`, or convert it to PEM with an unencrypted key. A read error
names the file, says what is wrong with it, and never quotes it.

A guest account needs `tenant_id`, the tenant of the database. Without `client_id`, a person
signs in through Microsoft's development application, which is fine on a developer's machine.

**One sign-in per datasource.** Each datasource has one credential and one token cache, and
shares neither with another datasource. A managed identity's tokens are also cached by MSAL, in
one cache for the whole process, so datasources that sign in as the same managed identity with
the same scope may be handed the same token. With `token_cache: persistent` (see below), every
datasource of every gorgany app on the machine keeps its tokens in one store of the operating
system's credential store, so datasources that sign in as the same account, with the same
application and scope, may be handed the same token too. Every connection signs in with the same
token:

- From the token's `RefreshOn` time, which MSAL sets to half the life of a long-lived token such
  as a managed identity's, the token is renewed in the background while connections keep using
  it. A renewal that fails there is tried again 30 seconds later, not by every connection.
- Within five minutes of the expiry, a connection waits for the renewal. If the renewal fails
  while the token is still valid for a minute, the connection uses the old token.
- Connections that open together share one request for a token.

So an outage of Entra ID or of the instance metadata service fails connections only when the
token it last issued is about to expire.

`interactive` and `device_code` ask the person once per datasource: at boot, or on the first
connection when `lazy_connect` is set. An app with two such datasources asks twice, and with
`lazy_connect` on the second, the second prompt waits for its first query. After the sign-in, a
token that cannot be renewed without the person fails with "the interactive sign-in for … could
not be renewed silently", which names the likely causes, and the next connection tries again. It
never opens another prompt. If the error persists, restart the process to sign in again, or use
`azure_cli`. A sign-in that failed has signed nobody in, so the next connection asks again.
`azure_cli` never prompts.

### Remembering the sign-in across restarts (development)

By default, `interactive` and `device_code` keep their tokens in memory, so every start of the
process asks the person again. `token_cache: persistent` keeps them in the operating system's
credential store instead, and the next start signs in without asking:

```yaml
    auth:
      method: interactive
      token_cache: persistent          # memory (the default) | persistent
```

The store links the keychain, which an app that does not use it should not have to link, so it
comes from a package of its own. Import it next to the `azuread` import:

```go
    _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread/persistentcache"
```

Without it, `token_cache: persistent` fails the boot, and the error quotes the import line.

- **macOS:** the login keychain, which needs cgo: a C compiler (the Xcode command-line tools)
  and `CGO_ENABLED=1`, the default where one is installed. macOS may ask to let the binary use
  the keychain item, and ask again after it is rebuilt.
- **Linux:** a file under `$XDG_CACHE_HOME` (or `~/.cache`), encrypted with a key in the
  kernel's user keyring. No cgo is needed, but a container's default seccomp profile blocks the
  keyring, and the key is lost at reboot, after which the person signs in once more.
- **Windows:** a file under `%LOCALAPPDATA%`, encrypted with DPAPI for the signed-in user.

Built for macOS without cgo, or for another system, the package still compiles, and
`token_cache: persistent` fails the boot with the reason.

The first start asks the person, as without the cache, and writes an authentication record: a
small JSON file that says which account the cached tokens belong to. It holds no token and no
secret, only the authority, tenant, application, account ID and username; the tokens stay in
the credential store. The file is written readable by its owner only, and replaced whole, never
rewritten in place. By default it goes under the user's cache directory (`~/Library/Caches` on
macOS), as `gorgany/azuread/<hash>.json`, with a hash of the host, database, username,
`client_id` and `tenant_id`; a directory it has to create for it is its owner's alone. Set
`authentication_record_path` to put it somewhere else; it is refused without
`token_cache: persistent`, which is the only cache that reads it. A directory that already exists
is left as it is, so put the file in one only you can write: the record decides which of your
cached accounts signs in, and a record another user owns, or others can write, is ignored with a
warning, and the person is asked again.

A record for another account than `username` names is ignored, and the person is asked again, so
the datasource never signs in silently as someone the config does not name. The application and
the tenant are compared only where the config names them as IDs: `client_id` when it is set, and
`tenant_id` when it is a tenant ID rather than a domain name or `organizations`. `username` is only
the account the sign-in page suggests, and `device_code` does not read it, so the person may sign
in as another account, or under a sign-in name other than the alias configured. That sign-in is
not remembered, a warning says so, and the next start asks again; set `username` to the account's
sign-in name, or remove it.

With a record, a start first signs in silently from the cache, bounded like a renewal: by
`login_timeout` or 60 seconds, whichever is shorter. If the cache cannot answer, because the
refresh token expired or was revoked, the start asks the person once, as without the cache, within
what is left of `login_timeout`. If the silent sign-in runs out of time, or the datasource is
closed first, nobody is asked, and the error says the sign-in could not be resumed. If the record
cannot be written, the sign-in still stands, a warning says so, and the next start asks again.

This is for development machines only. The production image is built with `CGO_ENABLED=0` for
`scratch`, which has no credential store and no person to ask.

### Sovereign clouds

The cloud follows the host. So does the token scope, and so does the sign-in authority, because
a server in one cloud refuses a token issued by another.

| Host suffix | Cloud | Scope |
|---|---|---|
| `.database.windows.net`, and Synapse and Fabric SQL endpoints | public | `https://database.windows.net/.default` |
| `.database.usgovcloudapi.net` | US Government | `https://database.usgovcloudapi.net/.default` |
| `.database.chinacloudapi.cn` | China | `https://database.chinacloudapi.cn/.default` |

The host decides, and `AZURE_AUTHORITY_HOST` does not override it. On an Azure SQL host, an
`auth.scope` that names another cloud's Azure SQL, such as `https://database.usgovcloudapi.net`
on a `.database.windows.net` host, is refused at boot. A host that is not an Azure SQL endpoint,
such as a private endpoint behind a DNS name of its own or an IP address, names no cloud. For
such a host, an `auth.scope` that names a cloud's Azure SQL selects that cloud's authority too,
and without one the cloud is public. `azure_cli` has no authority option of its own. Select the
cloud with `az cloud set --name AzureUSGovernment` (or `AzureChinaCloud`) before `az login`.
`managed_identity` has no authority to choose either: it asks the platform's identity endpoint,
which issues tokens in the resource's own cloud, and only the scope it asks for follows the
host.

### Development and deployed apps

`interactive`, `device_code` and `azure_cli` are for development. The production image is
`scratch` ([DEPLOYMENT.md](DEPLOYMENT.md#the-image)), which has no browser, no terminal anyone
watches, and no `az`.

A deployed app signs in with one of these:

- **`managed_identity`**, on an Azure resource with a managed identity: App Service, Functions,
  Container Apps, a VM or scale set, or an AKS node. There is nothing to store or rotate. Leave
  the ID keys empty for the resource's default identity, usually its system-assigned one, or set
  one of `client_id`, `resource_id` or `object_id` to select a user-assigned one. Two are
  refused, because each names an identity and only one signs in.
- **`workload_identity`**, in a Kubernetes pod whose service account is federated with an app
  registration or a user-assigned identity. It exchanges the service-account token Kubernetes
  projects into the pod, so there is nothing to store or rotate either. On AKS, the workload
  identity webhook puts the tenant, the token file and the client ID from the service account's
  `azure.workload.identity/client-id` annotation in the environment of a pod labelled
  `azure.workload.identity/use: "true"`, and `method: workload_identity` alone is enough.
  Elsewhere, set `client_id`, `tenant_id` and `token_file_path`. One that neither the config nor
  the environment supplies stops the boot, and the error names the key and the variable.
- **`service_principal`**, with the secret in the host's `app.env` or a certificate mounted
  outside the repository tree. The app runs as uid 65532, so that user must be able to read the
  certificate. Rotating the secret means updating `app.env` and restarting.
- **`azure_default`**, which tries the environment's service principal, a workload identity and a
  managed identity in turn, and developer tools after them (see below). Prefer one of the
  methods above where one fits. Each is a single credential: it reads only what the table below
  says, never falls through to a developer tool, and fails with its own error rather than one
  from every credential in the chain.

To switch method per environment, write `method: ${LEGACY_DB_AUTH_METHOD}`. Each method refuses
the keys it does not take, and an empty value counts as unset, so every key in the block must be
empty wherever the method in use does not take it. To include `service_principal`, add
`client_id: ${LEGACY_DB_CLIENT_ID}` and `client_secret: ${LEGACY_DB_CLIENT_SECRET}` (or
`certificate_path`). An unset `client_secret` placeholder stops the boot in every environment,
even one whose method takes no secret, so each environment's env file defines
`LEGACY_DB_CLIENT_SECRET`, empty where it is not used. `azure_default` refuses `client_id`, so
leave `LEGACY_DB_CLIENT_ID` empty where it runs. `managed_identity` refuses `tenant_id`, because
a managed identity signs in to the one tenant its subscription trusts, so leave
`LEGACY_DB_TENANT_ID` empty where it runs.

Besides the config, each deployed method reads some variables by itself:

| Method | Reads from the environment |
|---|---|
| `managed_identity` | No `AZURE_*` variable, not even `AZURE_CLIENT_ID`. It finds the platform's identity endpoint in the variables the platform sets: `IDENTITY_ENDPOINT` and `IDENTITY_HEADER` on App Service, Functions and Container Apps (with `IDENTITY_SERVER_THUMBPRINT`, Service Fabric), `MSI_ENDPOINT` in Cloud Shell (with `MSI_SECRET`, Azure ML, which signs in as the identity `DEFAULT_IDENTITY_CLIENT_ID` names when no ID key is set), `IDENTITY_ENDPOINT` with `IMDS_ENDPOINT`, or the agent's `himds` file even with neither set, on Azure Arc, and otherwise the instance metadata service. |
| `workload_identity` | `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE`, each only when `client_id`, `tenant_id` or `token_file_path` is empty. |
| `service_principal` | Nothing it signs in with; that all comes from `auth`. |
| `azure_default` | Everything above, and `AZURE_CLIENT_SECRET`, `AZURE_CLIENT_CERTIFICATE_PATH`, `AZURE_CLIENT_CERTIFICATE_PASSWORD`, `AZURE_TOKEN_CREDENTIALS`, `AZURE_ADDITIONALLY_ALLOWED_TENANTS` and others. Its managed identity takes a user-assigned identity's client ID from `AZURE_CLIENT_ID`. |

`service_principal` and `workload_identity` also read `AZURE_REGIONAL_AUTHORITY_NAME`, which sends
their token requests to a regional Entra ID endpoint when it is set. No method reads
`AZURE_AUTHORITY_HOST`, which the AKS webhook sets too: the host decides the cloud (see
"Sovereign clouds").

`managed_identity` ignoring `AZURE_CLIENT_ID` matters in a pod the workload identity webhook
mutated, where that variable names the workload's identity: `managed_identity` with no ID key
still signs in as the node's default identity. Not every platform can select a user-assigned
identity. Cloud Shell and Azure Arc have none, Service Fabric takes the identity from the
cluster's configuration, and Azure ML selects one by client ID only, and with no ID key signs in
as the one `DEFAULT_IDENTITY_CLIENT_ID` names. On those platforms, any other selection is
refused at boot.

`azure_default`'s DefaultAzureCredential tries these credentials, in order, and uses the first
that answers:

1. the environment's service principal;
2. workload identity;
3. managed identity;
4. the Azure CLI;
5. the Azure Developer CLI;
6. PowerShell.

A developer tool late in the chain can therefore sign a deployed app in as a person. Set
`AZURE_TOKEN_CREDENTIALS=prod` in deployment to keep the chain to the first three.

`auth.tenant_id` reaches only workload identity and the developer tools in that chain. The
environment's service principal takes its tenant from `AZURE_TENANT_ID`, and a managed identity
has one tenant, so neither reads `tenant_id`.

## Azure notes

- **No named instances.** An `instance` is dropped with a warning, and the connection goes to
  the port.
- **ApplicationIntent is routing, not a guard.** `read_only: true` sends
  `ApplicationIntent=ReadOnly`, which routes the connection to a readable secondary where the
  tier has one. Refusing writes is `read_only`'s job ([DIALECTS.md](DIALECTS.md)).
  `options.application_intent: ReadWrite` keeps the primary under `read_only: true`. `ReadOnly`
  without `read_only: true` is refused, because every write would fail on the node it routes
  to.
- **Contained users.** The DBA creates the principal in the database itself, not in `master`,
  and grants it only what the app needs:

  ```sql
  CREATE USER [user@example.com] FROM EXTERNAL PROVIDER;
  ALTER ROLE db_datareader ADD MEMBER [user@example.com];
  -- ALTER ROLE db_datawriter ADD MEMBER [user@example.com];  -- when the app writes
  ```

  Never grant `db_ddladmin` or `db_owner`. The guards behind `external_schema` and `read_only`
  are a safety net, and the principal's rights are the guarantee. A server firewall rule must
  admit the client.
- **Resume and failover.** A serverless database resuming, or a gateway failing over, refuses
  logins for seconds. The boot retries those errors (40613, 40501, 40197 and others) for about
  15 seconds. A failed login (18456) or a database the login cannot open (4060) fails at once.

## Types

The datasource sets go-mssqldb's `guid conversion`, so a `uniqueidentifier` scans into a
`uuid.UUID` (`github.com/google/uuid`) in the order every other client shows. Use `uuid.UUID`
and `uuid.NullUUID` in models.

**Do not use `mssql.UniqueIdentifier` or `mssql.NullUniqueIdentifier`.** They reorder the bytes
themselves, so with the conversion on they read every GUID with its first three groups reversed,
and they bind a value that matches nothing. Neither fails with an error. Set
`options.guid_conversion: false` only for code built on go-mssqldb's types.

DIALECTS.md, "Executed differently", covers how times, strings, `[]byte` and JSON are bound.

## A second, externally owned datasource

Every `cli` run boots every datasource: `db:migrate` and `db:seed` in the deploy steps,
`make migrate`, and every other command. Without `lazy_connect`, each of them dials the external
database at boot, and with `interactive` each opens a browser.

Set `lazy_connect: true` on a secondary external datasource. The constructor then opens nothing.
The first query signs in, bounded by `login_timeout`, and then connects. With `interactive`,
that first query waits for the person. A shutdown does not wait for the sign-in: `Close`
abandons it. The request that started it holds the HTTP drain until the request ends or the
shutdown deadline passes.

`external_schema: true` keeps `db:migrate`, `db:seed` and `db:diff` away from the database.
DEPLOYMENT.md, "More than one datasource", says what that datasource gets in deployment:
no migrate or seed service.

## SQL Server as the owned `default`

SQL Server can be the database gorgany owns: the `default` that holds the app's tables, its
`migrations` and `seeders` bookkeeping, and database sessions. Configure it like any other
datasource, without `external_schema` and `read_only`:

```yaml
databases:
  default:
    driver: sqlserver_gorm
    host: ${DB_HOST}
    port: 1433
    db: ${DB_NAME}
    username: ${DB_USER}
    password: ${DB_PASSWORD}           # or an auth block; see "Signing in"
```

The principal needs DDL rights in that database as well as reads and writes, since
`db:migrate` creates tables: `db_ddladmin` with `db_datareader` and `db_datawriter`, or
`db_owner`. That is the opposite of the advice for an externally owned database under "Azure
notes", and the reason to keep the two apart.

Up to and including v2.4.3 there is no SQL Server engine, so nothing below applies to them.

### Migrations and seeders

`db:migrate` and `db:seed` work as they do on Postgres (PROJECT_STRUCTURE.md, "Migrations and
seeders"). SQL Server's DDL is transactional, so a migration and its row in `migrations` commit
together or not at all, and `db:migrate down` drops a migration's schema and its row together.
Every connection runs `SET XACT_ABORT ON`, so a statement that fails inside a migration dooms
the transaction even if the migration swallows the error: the migration fails instead of
committing the statements around it. A seeder's rows and its row in `seeders` commit together
as well.

`migrations` and `seeders` key each row by a unique name, which gorm declares as
`nvarchar(256)`. `db:migrate` and `db:seed` look for those two tables, and the sessions
migrations for `sessions`, where the unqualified names resolve: in the login's default schema,
then `dbo`. A table of the same name in another schema of the database, in any case, such as
one an externally owned schema keeps beside gorgany's, is neither read nor changed.

Writing a migration by hand:

- T-SQL has no `CREATE TABLE IF NOT EXISTS`. Guard it with `IF OBJECT_ID(N'<table>', N'U') IS
  NULL`. `DROP TABLE IF EXISTS` works.
- gorm's `Migrator().HasTable` and `HasColumn` find a table or column of that name in any
  schema of the database, and the default collation ignores case. Where another schema may
  use the same name, ask `OBJECT_ID(N'<table>', N'U')` and `COL_LENGTH(N'<table>',
  N'<column>')` instead, which resolve the name as the unqualified DDL after them does.
- A string column that a gorm model tags `uniqueIndex` needs a `size`. Without one gorm declares
  it `nvarchar(max)`, which cannot be indexed, and the migration fails with Msg 1919. The
  `unique` and `index` tags get `nvarchar(256)` without a size.
- A column added with a default gets a DEFAULT constraint that SQL Server names itself, and
  `ALTER TABLE … DROP COLUMN` is refused while it exists (Msg 5074, then 4922). A `Down()` that
  drops such a column drops the constraint first. The framework's sessions version migration
  does it with this batch, in `@sql` because `EXEC ( … )` cannot call `QUOTENAME`:

  ```sql
  DECLARE @df sysname, @sql nvarchar(max);
  SELECT @df = dc.name FROM sys.default_constraints AS dc
  JOIN sys.columns AS c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id
  WHERE dc.parent_object_id = OBJECT_ID(N'sessions') AND c.name = N'version';
  IF @df IS NOT NULL
  BEGIN
      SET @sql = N'ALTER TABLE [sessions] DROP CONSTRAINT ' + QUOTENAME(@df);
      EXEC sp_executesql @sql;
  END
  ```

  Pass it to `dbGorm.Exec` with no arguments. gorm then leaves `@df` and `@sql` as written.

### `db:diff`

`db:diff` runs on a SQL Server datasource. `command/db.TransactionalDDLDialects` lists it
beside Postgres, because the `CREATE TABLE` and `ALTER TABLE` statements the diff runs to find
differences roll back with its transaction there too. The draft differs from a Postgres one in
two places:

- The struct column of a domain another one extends is added as
  `ALTER TABLE [<table>] ADD [<column>] nvarchar(255) NULL`.
- gorm stores a column comment with `sp_addextendedproperty`, and binds the schema, table and
  column names as arguments. The draft runs each statement without arguments, so the diff
  writes those names into the statement as string literals. A statement that binds anything
  other than a string stops the diff before it writes a draft, with an error that names the
  statement; write that change by hand.

The diff asks gorm's migrator whether each domain's table and columns exist, and on SQL Server
the migrator finds them by name in any schema of the database. A domain whose table name
another schema also uses can be compared with the other schema's table, so the draft can miss
changes it needs, or the diff fails. Write that domain's migration by hand. Review the draft as
PROJECT_STRUCTURE.md says.

### Database sessions

With `auth.session.storage: database` on a SQL Server `default`, the two sessions migrations
create `sessions` with SQL Server's own types. `id` and `user_id` are `nvarchar(255)`, and the
attribute bag is `nvarchar(max)`, because `varchar` and `text` store only the characters of
the column collation's code page. The timestamps are `datetimeoffset`.
`migration.SessionsTableModelFor("sqlserver")` returns the model the migration uses there.

The expired-session sweep runs `auth.BatchedExpiredDeleteSQLServer`:

```sql
DELETE TOP (?) FROM [sessions] WHERE [expiry] < SYSDATETIMEOFFSET()
```

`SYSDATETIMEOFFSET()` carries its offset, so the sweep compares instants and the server's time
zone does not matter. `auth.BatchedExpiredDeleteSQLFor(dialect)` returns the statement for a
dialect name, for an app that sweeps from a job of its own. Postgres and MySQL keep
`auth.BatchedExpiredDeleteSQL`.

Several replicas run every job, the sweep included. DEPLOYMENT.md, "More than one instance",
gives SQL Server's lock for a job that must run once.

### Tests

testsupport runs an app's integration tests against SQL Server. It creates the test database
through `master`, empties tables with a sequence of its own, and needs the driver imported into
the test binary. [TESTING.md](TESTING.md#sql-server) has the details.
