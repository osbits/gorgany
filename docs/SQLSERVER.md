# SQL Server and Azure SQL

The `sqlserver_gorm` driver connects to SQL Server 2016 or newer and to Azure SQL Database. It
signs in with a SQL login, or with Microsoft Entra ID (formerly Azure Active Directory). This
document covers connecting and signing in. What the dialect refuses and translates is in
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
import line.

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
      method: ${LEGACY_DB_AUTH_METHOD} # interactive | azure_cli (development); azure_default (deployed)
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
| Authentication: Managed Identity, Default | `auth.method: azure_default` | |
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
| `interactive` | a person, in the system browser | | `tenant_id`, `client_id`, `redirect_url`; `username` is the suggested account | development |
| `device_code` | a person, with a code the process prints | | `tenant_id`, `client_id` | development |
| `azure_cli` | the account `az login` chose | | `tenant_id` | development |
| `azure_default` | whatever azidentity's DefaultAzureCredential finds | | `tenant_id`, for some of the credentials it tries (see "Deployed apps") | deployed |
| `service_principal` | an app registration | `tenant_id`, `client_id`, and `client_secret` or `certificate_path` | `certificate_password`, `send_certificate_chain` | deployed |

Every Entra ID method also takes two keys:

- `scope` overrides the token scope, which is otherwise derived from the host.
- `login_timeout`, in seconds, bounds one sign-in. The default is 300 for `interactive` and
  `device_code`, and 60 for the others.

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
shares neither with another datasource. Every connection signs in with the same token:

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

### Development and deployed apps

`interactive`, `device_code` and `azure_cli` are for development. The production image is
`scratch` ([DEPLOYMENT.md](DEPLOYMENT.md#the-image)), which has no browser, no terminal anyone
watches, and no `az`.

A deployed app has two choices:

- **`azure_default`**, which picks up a managed identity or a workload identity, or a service
  principal from `AZURE_*` variables.
- **`service_principal`**, with the secret in the host's `app.env` or a certificate mounted
  outside the repository tree. The app runs as uid 65532, so that user must be able to read the
  certificate. Rotating the secret means updating `app.env` and restarting.

To switch method per environment, write `method: ${LEGACY_DB_AUTH_METHOD}`. Each method refuses
the keys it does not take, and an empty value counts as unset, so every key in the block must be
empty wherever the method in use does not take it. To include `service_principal`, add
`client_id: ${LEGACY_DB_CLIENT_ID}` and `client_secret: ${LEGACY_DB_CLIENT_SECRET}` (or
`certificate_path`). An unset `client_secret` placeholder stops the boot in every environment,
even one whose method takes no secret, so each environment's env file defines
`LEGACY_DB_CLIENT_SECRET`, empty where it is not used. `azure_default` refuses `client_id`, so
leave `LEGACY_DB_CLIENT_ID` empty where it runs.

azidentity reads some variables without being told to: `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`,
`AZURE_CLIENT_SECRET`, `AZURE_CLIENT_CERTIFICATE_PATH`, `AZURE_FEDERATED_TOKEN_FILE`,
`AZURE_TOKEN_CREDENTIALS` and others. DefaultAzureCredential tries these, in order, and uses the
first that answers:

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
