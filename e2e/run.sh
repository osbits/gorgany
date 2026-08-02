#!/bin/sh
set -eu

compose_file="e2e/docker-compose.yml"

# cleanup carries the real outcome out of the trap instead of leaving it to the shell.
#
# An EXIT trap runs after the shell has already decided to exit, and what the script's own
# status is afterwards depends on which sh is installed: POSIX settled only late on
# preserving $? across a trap that does not itself call `exit`, and shells predating that
# reported the status of the last command in the trap body. The teardown below ends in
# `|| true` — it has to, since tearing down a stack that never came up is not an error — so
# on any such shell this harness reported success for every possible failure. That was
# observed rather than feared: a base image that would not pull printed "failed to solve",
# no test executed at all, and the script still exited 0.
#
# Reading $? on the trap's first line and exiting with it on the last makes the status the
# script's own rather than the teardown's, on every shell.
cleanup() {
  status=$?

  # The run that failed is the one whose logs are worth reading, and `down` deletes the
  # containers holding them, so they are dumped first.
  if [ "$status" -ne 0 ]; then
    printf '\n===== e2e harness failed with status %s; container state and logs follow =====\n' "$status" >&2
    docker compose -f "$compose_file" ps -a >&2 || true
    docker compose -f "$compose_file" logs --no-color --timestamps --tail=200 >&2 || true
  fi

  docker compose -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true

  exit "$status"
}

trap cleanup EXIT

docker compose -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true

docker compose -f "$compose_file" build app-migrate app-seed app-server runner

# --wait is the other half of making these steps able to fail. Plain `up -d` reports success
# as soon as a container has been *started*, which it does even for a process that exits a
# moment later, so a database that died while initialising or a fixture app that panicked on
# boot left the harness walking on to the next step against a stack that was not running.
# --wait blocks on each service's healthcheck and exits non-zero when one of them stops.
docker compose -f "$compose_file" up -d --wait postgres postgres-live mysql-live

docker compose -f "$compose_file" run --rm app-migrate
docker compose -f "$compose_file" run --rm app-seed

docker compose -f "$compose_file" up -d --wait app-server

# The suite runs under E2E_REQUIRE_LIVE=1, set in the compose file. Every case in it gates
# itself on a dependency it can reach and skips when it cannot, so a green run used to be
# indistinguishable from a run in which nothing executed; under that variable a case that
# cannot reach its dependency fails, and a run that executed no case at all fails too.
docker compose -f "$compose_file" run --rm runner
