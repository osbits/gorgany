# Architecture

Replace each paragraph with the truth about this service. Keep it short enough to read
in five minutes; link to `domain/` for detail.

## Purpose and boundaries

What the service owns, who calls it, and what it calls. A context diagram is worth
more here than prose.

## Binaries

- `cmd/app` serves HTTP. `app healthcheck` probes `/readyz` and boots nothing.
- `cmd/cli` runs one console command and exits. It never starts the job scheduler.

## Boot order

`pkg/provider/bootstrap.go` lists the providers each binary boots. Every `Register` runs
first, in list order, then every `Boot`. Note here anything that depends on the order.

## Data

Datasources, the tables that matter, and which package owns each. Migrations are in
`db/migration`, applied in the order `All()` lists them.

## Background work

Jobs (server only), event subscribers, and anything else that runs outside a request.

## Decisions

See [adr/](adr/).
