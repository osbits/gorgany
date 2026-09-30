# SQL Server and Azure SQL

The `sqlserver_gorm` driver connects to SQL Server 2016 or newer and to Azure SQL Database. It
signs in with a SQL login, or with Microsoft Entra ID (formerly Azure Active Directory). Up to
and including v2.4.3 there is no SQL Server engine, so nothing here applies to those versions.

A SQL Server datasource plays one of two roles:

- **Externally owned.** Another system owns the schema: an EF Core application, its
  migrations, or a DBA. The app reads and writes rows through the query builder and the ORM,
  and gorgany never runs DDL, migrations, seeders or session bookkeeping there. Mark it
  `external_schema: true`, and `read_only: true` until its models are verified. This is the
  usual reason to add SQL Server to an app, beside the `default` it already owns.
- **Gorgany-owned**, as `default` or under another name. It migrates, seeds, diffs, stores and
  sweeps database sessions, and runs under testsupport, as Postgres does.

What the dialect refuses and translates is in
[DIALECTS.md](DIALECTS.md#what-sql-server-refuses-and-translates). How a second datasource is
deployed is in [DEPLOYMENT.md](DEPLOYMENT.md#more-than-one-datasource).

- [Imports](#imports)
- [Configuration](#configuration): the keys, DataGrip and SSMS fields, `ssl`, `options`, the flags
- [Signing in](#signing-in): methods, development and deployed apps, the variables azidentity
  reads, the persistent cache, sovereign clouds
- [Adding an externally owned SQL Server datasource to an app](#adding-an-externally-owned-sql-server-datasource-to-an-app)
- [Mapping EF Core tables](#mapping-ef-core-tables)
- [Types](#types)
- [SQL Server as the owned `default`](#sql-server-as-the-owned-default)
- [Acceptance runbook](#acceptance-runbook)
- [Known limitations](#known-limitations)

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
on a development machine ([Remembering the sign-in across restarts](#remembering-the-sign-in-across-restarts-development)).

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
    host: ${LEGACY_DB_HOST}            # set_me.database.windows.net
    port: 1433
    db: ${LEGACY_DB_NAME}              # Example-db; hyphens are fine
    username: ${LEGACY_DB_USER}        # interactive: only the account the sign-in suggests
    external_schema: true              # another system owns the schema
    read_only: true                    # until VerifyModel is clean for every model; then false
    lazy_connect: true                 # CLI runs neither dial it nor prompt for it
    auth:
      # interactive | azure_cli (development); managed_identity | workload_identity (deployed)
      method: ${LEGACY_DB_AUTH_METHOD}
      tenant_id: ${LEGACY_DB_TENANT_ID}
    options:
      app_name: my-app
    properties:
      maxOpenConnections: 10
```

- The example host is `set_me.database.windows.net`, which resolves nowhere: an Azure SQL
  server name cannot contain `_`. A real-looking name such as `example.database.windows.net`
  can be someone else's server, and an Entra ID sign-in would send it your access token.
- Keep an owned `default`. The sessions table and the `migrations` and `seeders` bookkeeping
  live there, and `auth.session.storage: database` refuses a `default` marked
  `external_schema` or `read_only`.
- Write the flags as literals. A flag must be a boolean, and an unresolved placeholder reads
  as `""`, which fails the boot.
- Do not name your own placeholders `AZURE_*`. azidentity reads those variables by itself
  ([Variables each method reads](#variables-each-method-reads-by-itself)).

### From DataGrip or SSMS

Copy the connection field by field. A URL, JDBC string or connection string is not accepted,
because a string that sets a key twice has no rule for which value wins.

| DataGrip / SSMS field | gorgany key | Notes |
|---|---|---|
| Host, Server name | `host` | The name alone. `tcp:`, `,1433`, `:1433` and `\instance` are refused, and the refusal names the key each part belongs in. |
| Port | `port` | `0` or unset means 1433, or SQL Server Browser when an on-premises `instance` is set. |
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
| ApplicationIntent=ReadOnly | `read_only: true` | This sets the intent; see [the flags](#flags). |
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
| `guid_conversion` | `guid conversion` | boolean; default `true` (see [Types](#types)) |
| `trust_server_certificate` | `trustservercertificate` | boolean; default `false` |
| `hostname_in_certificate`, `certificate`, `server_certificate`, `tls_min` | same, without `_` | TLS verification |
| `dial_timeout`, `connection_timeout`, `keep_alive` | `dial timeout`, `connection timeout`, `keepalive` | seconds; not defaulted |
| `packet_size`, `workstation_id`, `protocol`, `pipe`, `timezone`, `no_trace_id`, `disable_retry`, `log` | as go-mssqldb | as go-mssqldb |
| `failover_partner`, `failover_port`, `failover_partner_spn`, `multi_subnet_failover`, `server_spn`, `epa_enabled` | as go-mssqldb | as go-mssqldb |

A key that a typed key owns is refused, and the error names the typed key. For example,
`database` goes in `db`, `user id` in `username`, `server` in `host`, `encrypt` in `ssl`, and
`fedauth`, `authentication` and `applicationclientid` in `auth`. `search_path` is refused too:
qualify another schema in `TableName()` instead. Pool settings that are left unset default to
10 open connections and 5 idle ones. An idle connection is closed after 5 minutes, and every
connection after 30.

### Flags

Three keys under `databases.<name>` decide what gorgany may do with a datasource. Every engine
reads them; this is what each does on SQL Server.

| Flag | What it does here |
|---|---|
| `external_schema: true` | `db:migrate`, `db:seed` and `db:diff` refuse the datasource before they send any SQL and exit 2, and so does every `db:migrate` or `db:seed` run while a registered migration or seeder targets it. On `default`, `db:migrate` leaves the sessions migrations out, and `auth.session.storage: database` refuses the boot. The connection's guard refuses DDL under T-SQL's rules: `CREATE`, `ALTER`, `DROP`, `TRUNCATE`, `GRANT`, `REVOKE` and `DENY` anywhere outside parentheses, `SELECT … INTO` a permanent table, `sp_rename` and the other procedures that change a schema or who may use it, `DBCC`, `ENABLE TRIGGER` and `DISABLE TRIGGER`, dynamic SQL (`EXEC (…)`, `sp_executesql`), and `OPENQUERY`, `OPENROWSET` and `OPENDATASOURCE`. `#temp` tables are exempt. A `Save` that would cascade into related entities is refused unless the model implements `orm.CascadingSaves`. Rows are read and written as usual. |
| `read_only: true` | The dialect refuses to render an `INSERT`, `UPDATE`, `DELETE` or upsert, and the connection's guard refuses a write that reaches gorm any other way, reading every statement word by word, the builder's included. It also refuses the table hints that lock. The db commands and database session storage refuse the datasource as for `external_schema`. Each refusal wraps `core.ErrReadOnly`. It also sets `ApplicationIntent=ReadOnly` (below). |
| `lazy_connect: true` | The constructor opens nothing: no sign-in and no ping at boot. The first query signs in, bounded by `auth.login_timeout`, and then connects, so an unreachable server or a credential that does not work surfaces there instead of failing the deploy. The sign-in finishes before the first connection is opened, as every connection takes its token before it dials, so a sign-in that waits on a person, as `interactive` and `device_code` do, cannot leave a connection idle for the Azure SQL gateway to drop. |

Both refusal flags are a safety net, not a permission. The guards recognise statements; they
do not see what server code does ([Known limitations](#known-limitations)). The principal's
rights are the guarantee: never grant `db_ddladmin` or `db_owner` on a database another system
owns, and grant `db_datawriter` only once the app writes.

**ApplicationIntent is routing, not a guard.** `read_only: true` sends
`ApplicationIntent=ReadOnly`, which routes the connection to a readable secondary where the
tier has one. `options.application_intent: ReadWrite` keeps the primary under `read_only: true`.
`ReadOnly` without `read_only: true` is refused, because every write would fail on the node it
routes to.

**`lazy_connect` on a secondary datasource.** Every `cli` run boots every datasource:
`db:migrate` and `db:seed` in the deploy steps, `make migrate`, and every other command.
Without `lazy_connect`, each of them dials the external database at boot, and with
`interactive` each opens a browser. With it, a request that waits on a lazy interactive sign-in
holds the HTTP drain ([Known limitations](#known-limitations)).

**Resume and failover.** A serverless database resuming, or a gateway failing over, refuses
logins for seconds. Without `lazy_connect`, the boot retries those errors (40613, 40501, 40197
and others) for about 15 seconds. A failed login (18456) or a database the login cannot open
(4060) fails at once.

## Signing in

`auth.method` selects the sign-in. Each method refuses any `auth` key it never uses.

| Method | Signs in as | Required | Optional | Where |
|---|---|---|---|---|
| `sql` (or no `auth`) | a SQL login | `username`, `password` | | anywhere |
| `interactive` | a person, in the system browser | | `tenant_id`, `client_id`, `redirect_url`, `token_cache`, `authentication_record_path`; `username` is the suggested account | development |
| `device_code` | a person, with a code the process prints | | `tenant_id`, `client_id`, `token_cache`, `authentication_record_path` | development |
| `azure_cli` | the account `az login` chose | | `tenant_id` | development |
| `azure_default` | whatever azidentity's DefaultAzureCredential finds | | `tenant_id`, for some of the credentials it tries | deployed |
| `service_principal` | an app registration | `tenant_id`, `client_id`, and `client_secret` or `certificate_path` | `certificate_password`, `send_certificate_chain` | deployed |
| `managed_identity` | the managed identity of the Azure resource the app runs on | | at most one of `client_id`, `resource_id` and `object_id`, for a user-assigned identity; none is the resource's default identity, usually its system-assigned one | deployed, on Azure |
| `workload_identity` | the app registration or user-assigned identity a Kubernetes service account is federated with | | `tenant_id`, `client_id`, `token_file_path`; each one left empty comes from the environment | deployed, on Kubernetes |

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
Where Conditional Access blocks that application, register one and set `client_id`, or use
`azure_cli`.

**One sign-in per datasource.** Each datasource has one credential and one token cache, and
shares neither with another datasource. A managed identity's tokens are also cached by MSAL, in
one cache for the whole process, so datasources that sign in as the same managed identity with
the same scope may be handed the same token. With `token_cache: persistent`, every datasource of
every gorgany app on the machine keeps its tokens in one store of the operating system's
credential store, so datasources that sign in as the same account, with the same application
and scope, may be handed the same token too. Every connection signs in with the same token:

- From the token's `RefreshOn` time, which MSAL sets to half the life of a long-lived token such
  as a managed identity's, the token is renewed in the background while connections keep using
  it. A renewal that fails there is tried again 30 seconds later, not by every connection.
- Within five minutes of the expiry, a connection waits for the renewal. If the renewal fails
  while the token is still valid for a minute, the connection uses the old token.
- Connections that open together share one request for a token.
- A connection takes its token before it dials, and signs in with that token once its handshake
  is done. One whose token has expired by then, after a handshake that slow, takes a renewed
  token instead of sending one the server would refuse.

So an outage of Entra ID or of the instance metadata service fails connections only when the
token it last issued is about to expire.

`interactive` and `device_code` ask the person once per datasource: at boot, or on the first
query when `lazy_connect` is set, before its connection is opened. An app with two such
datasources asks twice, and with `lazy_connect` on the second, the second prompt waits for its
first query. After the sign-in, a token that cannot be renewed without the person fails with
"the interactive sign-in for … could not be renewed silently", which names the likely causes,
and the next connection tries again. It never opens another prompt. If the error persists,
restart the process to sign in again, or use `azure_cli`. A sign-in that failed has signed
nobody in, so the next connection asks again. `azure_cli` never prompts.

### Development and deployed apps

`interactive`, `device_code` and `azure_cli` are for development, and so is
`token_cache: persistent`. The production image is `scratch`
([DEPLOYMENT.md](DEPLOYMENT.md#the-image)), which has no browser, no terminal anyone watches, no
`az` and no credential store.

A deployed app signs in with a SQL login, its password in the host's `app.env`, or with one of
these Entra ID methods:

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
  certificate. To rotate the secret, add a new one to the app registration, put it in `app.env`,
  restart, and then delete the old one.
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

### Variables each method reads by itself

Besides the config, each deployed method reads some variables that no placeholder mentions.
List the ones your deployment sets in `docs/configuration.md` by hand
([DEPLOYMENT.md](DEPLOYMENT.md#configuration)).

| Method | Reads from the environment |
|---|---|
| `managed_identity` | No `AZURE_*` variable, not even `AZURE_CLIENT_ID`. It finds the platform's identity endpoint in the variables the platform sets: `IDENTITY_ENDPOINT` and `IDENTITY_HEADER` on App Service, Functions and Container Apps (with `IDENTITY_SERVER_THUMBPRINT`, Service Fabric), `MSI_ENDPOINT` in Cloud Shell (with `MSI_SECRET`, Azure ML, which signs in as the identity `DEFAULT_IDENTITY_CLIENT_ID` names when no ID key is set), `IDENTITY_ENDPOINT` with `IMDS_ENDPOINT`, or the agent's `himds` file even with neither set, on Azure Arc, and otherwise the instance metadata service. |
| `workload_identity` | `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE`, each only when `client_id`, `tenant_id` or `token_file_path` is empty. |
| `service_principal` | Nothing it signs in with; that all comes from `auth`. |
| `azure_default` | Everything above, and `AZURE_CLIENT_SECRET`, `AZURE_CLIENT_CERTIFICATE_PATH`, `AZURE_CLIENT_CERTIFICATE_PASSWORD`, `AZURE_TOKEN_CREDENTIALS`, `AZURE_ADDITIONALLY_ALLOWED_TENANTS` and others. Its managed identity takes a user-assigned identity's client ID from `AZURE_CLIENT_ID`. |

`service_principal` and `workload_identity` also read `AZURE_REGIONAL_AUTHORITY_NAME`, which sends
their token requests to a regional Entra ID endpoint when it is set. No method reads
`AZURE_AUTHORITY_HOST`, which the AKS webhook sets too: the host decides the cloud (see
[Sovereign clouds](#sovereign-clouds)).

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
cached accounts signs in. On macOS and Linux, a record another user owns, or others can write, is
ignored with a warning, and the person is asked again. Windows has no such check: the file's ACL,
which it inherits from its directory (the user's profile, for the default location), is what
protects it.

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

## Adding an externally owned SQL Server datasource to an app

These steps add a database another system owns, such as an EF Core application's, to an app in
the [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md) layout, under the name `legacy`. Start with
`read_only: true`, and switch it off only once the [runbook](#acceptance-runbook) says so.

1. **`pkg/provider/bootstrap.go`:** the `azuread` import, or `driver/sqlserver` for a SQL
   login ([Imports](#imports)).
2. **`config/config.yml`:** the `legacy` block from [Configuration](#configuration), every
   per-environment value a whole-value placeholder, the flags as literals.
3. **`.env.sample`:** one line per new variable, each with its comment line (what it is,
   whether production needs it, whether it is secret), and values that are obviously fake:

   ```
   # legacy: SQL Server host (docs/SQLSERVER.md); production: yes; not secret
   LEGACY_DB_HOST=set_me.database.windows.net
   # legacy: database name; production: yes; not secret
   LEGACY_DB_NAME=Example-db
   # legacy: account an interactive sign-in suggests; development only; not secret
   LEGACY_DB_USER=user@example.com
   # legacy: auth.method; interactive or azure_cli here, managed_identity in production
   LEGACY_DB_AUTH_METHOD=azure_cli
   # legacy: Entra tenant, for guest accounts; empty where managed_identity runs; not secret
   LEGACY_DB_TENANT_ID=
   ```

4. **`docs/configuration.md`:** the same variables, and the `AZURE_*` variables the deployed
   method reads by itself ([the table](#variables-each-method-reads-by-itself)), such as
   `AZURE_TOKEN_CREDENTIALS=prod` for `azure_default`. The completeness test cannot see those.
5. **The DBA's side.** The principal is created in the database itself, not in `master`, and
   granted only what the app needs. A server firewall rule must admit the client.

   ```sql
   CREATE USER [user@example.com] FROM EXTERNAL PROVIDER;
   ALTER ROLE db_datareader ADD MEMBER [user@example.com];
   -- ALTER ROLE db_datawriter ADD MEMBER [user@example.com];  -- once the app writes
   ```

   Never `db_ddladmin` or `db_owner`. A managed identity or service principal is created the
   same way, under its name in Entra ID.
6. **`pkg/legacy`, a capability package** (PROJECT_STRUCTURE.md, "What goes where"), wired by
   `pkg/provider/legacy_provider.go`. It holds the models ([Mapping EF Core
   tables](#mapping-ef-core-tables)), a store that reads and writes them, and a session
   helper. Keep the models out of `./pkg/domain`: `domains:register` and `db:diff` treat every
   struct there as a table gorgany owns.

   ```go
   package legacy

   // Datasource is the name of the externally owned database under databases in config.yml.
   const Datasource = "legacy"

   // newSession opens a session on the legacy datasource, one per call.
   func newSession(databases core.IDBContext) (dbcore.ISession, error) {
       source := databases.GetDataSource(Datasource)
       if source == nil {
           return nil, fmt.Errorf("datasource %q is not configured", Datasource)
       }
       return source.NewSession()
   }
   ```

   The ORM runs on the session it is given, so this helper is what puts a model on `legacy`. A
   model's `DbConnectionName()` does not route it. Declare it anyway: `db:diff` is its only
   reader, and diffs a registered domain only against the datasource it names, so a model that
   is ever registered stays out of `default`'s drafts.
7. **`VerifyModel`**, from a console command in `pkg/command` that checks every model in
   `pkg/legacy` against the database and fails when anything is reported
   ([VerifyModel](#verifymodel)). It only reads, so it runs against production under
   `read_only: true`.
8. **Deployment.** Nothing is added to `deploy/compose.prod.yml`: no `migrate-legacy`,
   `seed-legacy` or backup service, since the owner migrates and backs up that database.
   Decide whether `/readyz` pings it. Both are in [DEPLOYMENT.md](DEPLOYMENT.md#more-than-one-datasource).
9. **Documentation.** The Data section of `docs/architecture.md` names the datasource, its
   owner and what the app does there, and an ADR records the decision
   (PROJECT_STRUCTURE.md, "Documentation in the repository").
10. **Tests.** The integration tier gets a second testsupport harness, from `testsupport.New`,
    on a local docker SQL Server database with `test` in its name, whose migration is a
    test-only fixture that creates the tables the models map, copied from the owner's schema
    ([TESTING.md](TESTING.md#sql-server)). The e2e stack may add SQL Server as the stubbed
    external system ([APP_TESTING.md](APP_TESTING.md#the-e2e-tier)).

## Mapping EF Core tables

Models are hand-written: a `TableName()` and a `gorm:"column:…"` tag on every field, in the
table's own spelling. There is no generator. Leave EF's navigation properties out: a relation
field makes `Save` cascade, which `external_schema` refuses. Load related rows with the related
model's own ORM.

This model maps `[dbo].[2024Orders]`: an `IDENTITY` key, a `uniqueidentifier` foreign key, a
GUID with a `NEWSEQUENTIALID()` default, a reserved-word column, a nullable `datetime2`, a
nullable `nvarchar` and a `rowversion`.

```go
type Orders2024 struct {
    orm.BaseEntity
    Id         int32         `gorm:"column:Id;primaryKey;autoIncrement"`     // gorm detects only ID/id by itself
    CustomerId uuid.UUID     `gorm:"column:CustomerId"`
    ManagerId  uuid.NullUUID `gorm:"column:ManagerId"`                       // NULLable
    PublicId   uuid.UUID     `gorm:"column:PublicId;->" grgorm:"readback"`   // server default, never written
    Order      int32         `gorm:"column:Order"`                           // [Order]
    ClosedAt   *time.Time    `gorm:"column:ClosedAt"`                        // NULLable datetime2, bound in UTC
    Notes      *string       `gorm:"column:Notes"`                           // NULLable nvarchar(max)
    RowVersion []byte        `gorm:"column:RowVersion;->" grgorm:"readback"` // re-read after every write
}

func (Orders2024) TableName() string        { return "dbo.2024Orders" } // [dbo].[2024Orders]
func (Orders2024) DbConnectionName() string { return "legacy" }
```

`TestEFShapes` in `db/sql/gorm/sqlserver/v2` pins the SQL the ORM sends for it, as go-mssqldb
receives it. `e2e/tests/live_sqlserver_ef_test.go` runs the same model, with a `decimal(18,2)`
column mapped as `Total string` beside it, against a real server:

```
SELECT TOP (1) * FROM [dbo].[2024Orders] WHERE [Id] = @p1
INSERT INTO [dbo].[2024Orders] ([CustomerId], [ManagerId], [Order], [ClosedAt], [Notes])
  OUTPUT INSERTED.[Id], INSERTED.[PublicId], INSERTED.[RowVersion] VALUES (@p1, @p2, @p3, @p4, @p5)
UPDATE [dbo].[2024Orders] SET [Order] = @p1 OUTPUT INSERTED.[PublicId], INSERTED.[RowVersion]
  WHERE [Id] = @p2 AND [RowVersion] = @p3
```

### Authoring rules

| Column | Go type and tags |
|---|---|
| `IDENTITY` key | `primaryKey;autoIncrement`, left zero on `Create`; `OUTPUT` reads it back |
| Key the app assigns, such as a GUID | `primaryKey;autoIncrement:false`, set before `Create` (`uuid.New()`) |
| Composite key | `primaryKey;autoIncrement:false` on every key column; `Find(id)` refuses it, so query with `FirstByQuery` and a condition per column |
| NULLable column | a pointer, `sql.Null*` or `uuid.NullUUID`. A plain type reads NULL as its zero value, and a full-row `Update` writes that back |
| `rowversion`, computed, temporal period column | `->` and `grgorm:"readback"`: never written, re-read after each write |
| Column with a server default the app never sets | `->` and `grgorm:"readback"`. `readback` alone does not stop the write that overrides the default |
| Non-key `IDENTITY` column | `->` and `grgorm:"readback"` |
| `varchar` column | `string`; see [Types](#types) for binding a lookup value |
| NULLable `varbinary` | `[]byte`; nil binds as a `varbinary` NULL |

### Guarded updates

A full-row `Update` writes every column the entity holds, including ones another system changed
since the entity was read. On a table another system writes, set the columns to write and a
guard on the rowversion:

```go
order, err := orders.Find(id)               // orders := orm.New[*Orders2024](session)
// …
order.Order = 4
meta := order.GetMeta()
meta.DirtyColumns = map[string]bool{"Order": true} // column names, as the table spells them
meta.UpdateGuard = []dbCore.Condition{
    &dbCore.BinaryCondition{Left: "RowVersion", Operator: "=", Right: order.RowVersion},
}
err = orders.Update(order)
switch {
case errors.Is(err, orm.ErrRowConflict): // someone changed the row: re-read and reconcile
case errors.Is(err, orm.ErrRowGone):     // the row was deleted: fail closed
}
```

The new rowversion is read back in the `UPDATE` itself, so the next guarded update can use it.
A dirty set that names a key column is refused: the ORM cannot move a row to a new key.

### Triggers

SQL Server refuses `OUTPUT` without `INTO` on a table with an enabled trigger on the statement's
own action (Msg 334). A trigger on `INSERT` refuses every `Create`, one on `UPDATE` every
`Update` of a model with `grgorm:"readback"` fields, and one on `DELETE` nothing the ORM sends.
A model on such a table says so:

```go
func (Orders2024) TableHasTriggers() bool { return true } // orm.TableWithTriggers
```

`Create` then reads the key with `SCOPE_IDENTITY()` in the same batch, the `INSERT`'s own key and
never a trigger's, and reads the other generated columns with a `SELECT` keyed on it. `Update`
re-reads the read-back fields with a `SELECT` after the write. Two consequences:

- Only an `IDENTITY` key can be learned that way. A key from a default or a sequence, a
  `NEWSEQUENTIALID()` key, and any key under an `INSTEAD OF INSERT` trigger must be assigned by
  the entity.
- The re-read is a statement of its own, so a write another session commits in between is what
  it reads. `Refresh` the entity before a guarded update that must not overwrite a write it has
  not seen.

`TableHasTriggers` is honoured only where the dialect reports that triggers block its
`RETURNING`, so a model shared with Postgres keeps `RETURNING` there.

### Cascades

On an `external_schema` datasource, `Save`, `Create`, `Update` and `UpdateExisting` refuse to
cascade into related entities, before anything is written, with an error that wraps
`core.ErrExternalSchema`. A model whose related tables are safe to write through the ORM
implements `orm.CascadingSaves` and returns true; it is asked of every entity the cascade would
reach. Leaving relation fields out of the models, as above, avoids the question.

### VerifyModel

`orm.VerifyModel(ctx, session, model)` compares a model with the table it maps and returns an
`orm.ModelProblem` for each disagreement, before any write has been sent. On SQL Server it reads
one catalog query, which finds the table as the server resolves the name in the ORM's own
statements. It generates nothing and runs no DDL, so it works under `read_only` and
`external_schema`.

```go
problems, err := orm.VerifyModel(ctx, session, &Orders2024{})
for _, p := range problems {
    log.Printf("%s.%s: %s: %s", p.Table, p.Column, p.Kind, p.Detail)
}
```

Each `Kind` is an `orm.Problem*` constant, and each `Detail` says what to change in the model:

| Kind | The model… |
|---|---|
| `missing_column` | maps a column the table does not have |
| `nullable_column_mapped_to_non_nullable_type` | reads a NULLable column into a type that cannot hold NULL |
| `generated_column_without_read_only_tag` | writes a rowversion, computed or period column (Msg 271, 272) |
| `identity_without_autoincrement` | writes an `IDENTITY` column (Msg 544) |
| `autoincrement_without_identity` | leaves a key to the server that nothing on the server generates |
| `server_default_written_by_insert` | overrides a server default on every `Create` |
| `enabled_triggers_without_TableHasTriggers` | will meet Msg 334 |
| `generated_key_unreadable_without_returning` | cannot learn a server-generated key without `OUTPUT` |
| `primary_key_mismatch` | addresses rows by other columns than the table's key |

An error means the check itself could not run: the model does not parse, or the table does not
exist or the login cannot see it. Run it for every model, and enable writes only once it
returns nothing for all of them.

## Types

The executor binds a few Go types differently from go-mssqldb's defaults.
[DIALECTS.md](DIALECTS.md#executed-differently) has why, and the test that pins each.

| Column | Go type | Notes |
|---|---|---|
| `uniqueidentifier` | `uuid.UUID` (`github.com/google/uuid`), `uuid.NullUUID` | The datasource sets go-mssqldb's `guid conversion`, so GUIDs read and bind in the order every other client shows. **Never `mssql.UniqueIdentifier` or `mssql.NullUniqueIdentifier`**: they reorder the bytes themselves, so they read every GUID with its first three groups reversed and bind one that matches nothing, with no error. Set `options.guid_conversion: false` only for code built on them. |
| `datetime2`, `datetime` | `time.Time`, `*time.Time` | Bound in UTC, since the server would keep a zoned time's wall clock and drop its offset. Stored as UTC, read back as UTC. |
| `datetimeoffset` | `time.Time` | Stored with `+00:00`. Bind `mssql.DateTimeOffset(t)` where the offset itself must be kept. |
| `nvarchar`, `nchar` | `string`, `*string` | |
| `varchar`, `char` | `string`, `*string` | A Go `string` binds as `nvarchar`, and the server then scans a `varchar` index instead of seeking it. Bind a lookup value you pass yourself as `mssql.VarChar(s)` (or `mssql.VarCharMax(s)`), which the executor passes through. |
| `varbinary`, `rowversion` | `[]byte` | Bound whole; nil binds as a `varbinary` NULL. |
| JSON in `nvarchar(max)` | `string`, `json.RawMessage` | A `json.RawMessage` binds as text, not `varbinary`. |
| `decimal`, `numeric` | `string` | Exact. `float64` rounds. |

## SQL Server as the owned `default`

SQL Server can be the database gorgany owns: the `default` that holds the app's tables, its
`migrations` and `seeders` bookkeeping, and database sessions. Configure it like any other
datasource, without `external_schema` and `read_only`:

```yaml
databases:
  default:
    driver: sqlserver_gorm
    host: ${DB_HOST}
    port: ${DB_PORT}
    db: ${DB_NAME}
    username: ${DB_USER}
    password: ${DB_PASSWORD}           # or an auth block; see "Signing in"
    options:
      trust_server_certificate: ${DB_TRUST_SERVER_CERT}
```

For a local docker SQL Server, `DB_HOST` is `127.0.0.1`, `DB_PORT` is `14330` as in
[TESTING.md](TESTING.md#quick-start), and `DB_TRUST_SERVER_CERT` is `true`. On Azure SQL,
`DB_PORT` is `1433` and `DB_TRUST_SERVER_CERT` must be `false`, not empty: `true` fails the boot
on an Azure host, and so does an empty option.

The principal needs DDL rights in that database as well as reads and writes, since
`db:migrate` creates tables: `db_ddladmin` with `db_datareader` and `db_datawriter`, or
`db_owner`. That is the opposite of the advice for an externally owned database, and the reason
to keep the two apart.

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

## Acceptance runbook

Run this before an app first reads an externally owned database in production, and again before
it first writes there. It uses the stand-ins of this page; substitute your own names, and keep
them out of the repository.

**0. Pre-flight**, in DataGrip or SSMS, signed in as the principal the app will use:

```sql
SELECT DB_NAME() AS [database], SUSER_SNAME() AS [login],
    DATABASEPROPERTYEX(DB_NAME(), 'Updateability') AS [updateability],
    IS_ROLEMEMBER('db_owner') AS [db_owner], IS_ROLEMEMBER('db_ddladmin') AS [db_ddladmin],
    HAS_PERMS_BY_NAME(DB_NAME(), 'DATABASE', 'CREATE TABLE') AS [can_create_table],
    IS_ROLEMEMBER('db_datareader') AS [db_datareader], IS_ROLEMEMBER('db_datawriter') AS [db_datawriter];

SELECT OBJECT_SCHEMA_NAME(tr.[parent_id]) AS [schema], OBJECT_NAME(tr.[parent_id]) AS [table],
    tr.[name] AS [trigger], te.[type_desc] AS [fires_on], tr.[is_instead_of_trigger]
FROM sys.triggers AS tr
JOIN sys.trigger_events AS te ON te.[object_id] = tr.[object_id]
WHERE tr.[parent_class] = 1 AND tr.[is_disabled] = 0
ORDER BY [schema], [table], [trigger];
```

- `db_owner`, `db_ddladmin` and `can_create_table` must be 0. Otherwise the guards are the only
  thing between the app and the schema; have the DBA take the rights away first.
- `updateability` must be `READ_WRITE` before writes are enabled. `READ_ONLY` is a geo-secondary
  or a read replica, which refuses every write whatever the app does.
- Every table a model maps that appears in the second list needs `TableHasTriggers`
  ([Triggers](#triggers)); `VerifyModel` checks this too.
- Note the row count and the newest `MigrationId` of `[dbo].[__EFMigrationsHistory]`.

**1. Boot** with `external_schema: true` and `read_only: true`, on a development machine. With
`interactive` the person is asked once per process, at boot or, under `lazy_connect`, at the
first query, and never again; `azure_cli` never asks. The only boot warnings are ones you expect, such as an ignored `instance`.

**2. Refusals.** Each of these exits 2, names `external_schema`, and sends no SQL:

```bash
go run ./cmd/cli db:migrate up --datasource=legacy
go run ./cmd/cli db:seed --datasource=legacy
go run ./cmd/cli db:diff --datasource=legacy
```

`db:migrate up` and `db:seed` on `default` still succeed. `__EFMigrationsHistory` is unchanged.

**3. Read probes.** Run the app's read paths against the database: a `Count()` and a `Find` of a
known key for every model, and a page read with `Limit` and `Offset`. Run the `VerifyModel`
command, and fix the models until it reports nothing.

**4. `read_only` proof.** A `Create` through the ORM fails with an error that wraps
`core.ErrReadOnly`, before anything is sent, and the table's row count is unchanged.

**5. Write rehearsal on a schema-only copy.** Script the schema without its data, with SSMS's
Generate Scripts or sqlpackage, into a database of your own, such as a local docker SQL Server's
`legacy_rehearsal`. A trigger that writes into another database needs that database too. Point
the datasource there with `read_only: false`. A local docker server takes no Entra ID sign-in, so
there the datasource signs in with a SQL login: `auth.method: sql` with the other `auth` keys
empty, a `password` key, the docker port, and `options.trust_server_certificate: true`, as for
[the owned `default`](#sql-server-as-the-owned-default). Run `VerifyModel`, and run every write
path the app has: `Create`, a guarded `Update`, the `ErrRowConflict` a stale copy gets, and
`Delete`. Check the rows the triggers write.

**6. Enabling writes.** The DBA adds the principal to `db_datawriter`. Run the pre-flight again,
then release `read_only: false`, keeping `external_schema: true`. Watch the log for
`ErrRowConflict`, `ErrRowGone` and SQL Server error hints.

**7. Rollback.** Release `read_only: true` again, and have the DBA drop the principal from
`db_datawriter`. That is a configuration change only: gorgany ran no migration there, so there
is nothing to roll back in the schema. Rows the app wrote stay; restoring them is the owner's
decision, from the owner's backups.

## Known limitations

- **The guards are a safety net.** They do not see what server code does on a statement's
  behalf: a procedure called by name, a function, a trigger. They pass a linked server's
  four-part name, whose statement they can read, and refuse `OPENQUERY`, `OPENROWSET` and
  `OPENDATASOURCE`, whose statement they cannot. A statement sent on the `*sql.DB` that
  `DB()` returns is not checked at all. On Postgres the external-schema guard refuses
  `SET session_replication_role`, but not `set_config('session_replication_role', …)`, which
  needs a superuser. The principal's rights are the guarantee.
- **No query timeouts.** The ORM runs every statement under `context.Background()`. Where a
  deadline matters, use the session's executor with a context of your own.
- **ORM writes are not transactional.** Each statement of a `Create`, `Update`, `Delete` or
  cascade commits on its own. Group writes that must commit together with the builder inside
  `session.Transaction`.
- **`default` only.** The `unique` validator and the CP relation pickers query `default`, so
  neither works for a `legacy` model.
- **RBAC.** `model/access_control.go` accepts only simple identifiers as table and field names,
  so it refuses a digit-leading table such as `2024Orders`.
- **Preload**, on every engine, reads the parent's key through a column name as a Go field
  name, so a model whose key field is not named like its column preloads nothing. Load
  relations with `LoadRelation`, or query the related model.
- **`ExecRaw` row counts include trigger rows**; builder writes count their own
  ([DIALECTS.md](DIALECTS.md#executed-differently)).
- **`ON CONFLICT … DO NOTHING`** fails a statement whose own rows share a key (Msg 2627), and
  concurrent multi-row upserts with overlapping keys can deadlock (Msg 1205)
  ([DIALECTS.md](DIALECTS.md#what-sql-server-refuses-and-translates), "Translated").
- **A server-generated key that is not the `IDENTITY`**, such as a `NEWSEQUENTIALID()` or
  sequence default, on a table with an enabled `UPDATE` trigger and a model with
  `grgorm:"readback"` fields: `Update` needs `TableHasTriggers`, which makes `Create` read
  without `OUTPUT`, and then the key cannot be read back. Assign the key client-side.
  `VerifyModel` reports it.
- **Parameter typing.** A Go `string` binds as `nvarchar` and a time as `datetimeoffset` in UTC
  ([Types](#types)).
- **`LIKE`** follows the column's collation, case included, and `[`, `]` and `^` are pattern
  characters ([DIALECTS.md](DIALECTS.md#what-sql-server-refuses-and-translates), "Translated").
- **Boot and shutdown.** Every `cli` command boots every datasource; use `lazy_connect` on
  external ones. A request blocked in a lazy interactive sign-in holds the HTTP drain until the
  shutdown deadline, which exits 1 and leaves the datasources open. The session sweep does not
  stop when its job is cancelled, on any engine
  ([DEPLOYMENT.md](DEPLOYMENT.md#more-than-one-instance)).
