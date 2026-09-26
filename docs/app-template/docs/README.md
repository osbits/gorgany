# myapp documentation

| Document | What it covers |
|----------|----------------|
| [architecture.md](architecture.md) | What the service is for, its boundaries, the provider order, datasources, jobs and events |
| [configuration.md](configuration.md) | Every environment variable (who reads it, its default, whether production needs it, whether it is secret), and the config keys with fixed values and why |
| [../api/openapi.yaml](../api/openapi.yaml) | The HTTP contract. [../api/routes.txt](../api/routes.txt) lists every route the app serves |
| [runbooks/deploy.md](runbooks/deploy.md) | Preparing a host; releasing: back up, migrate, seed, roll, verify; rolling back |
| [runbooks/rotate-secret.md](runbooks/rotate-secret.md) | Rotating the database password, signing keys and the deploy key |
| [adr/](adr/) | Decisions and why they were made, one file each |
| [../SECURITY.md](../SECURITY.md) | Reporting a vulnerability; where remediation records live |

Feature specifications go in `domain/`, one file per business area. Security reviews and
remediation records go in `security/`, dated. Both are tracked like code.
