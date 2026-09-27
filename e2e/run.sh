#!/bin/sh
# Runs the dockerised e2e suite, the fixture app and the live engines, from the repository
# root. The exit status is the suite's. Each run is its own compose project, torn down on exit:
#
#   sh e2e/run.sh                                     a project of its own, gorgany-e2e-<pid>
#   COMPOSE_PROJECT_NAME=e2e-$RUN_ID sh e2e/run.sh    a project the caller names, as CI may
#   E2E_REQUIRE_SQLSERVER=1 sh e2e/run.sh             SQL Server too, which is amd64-only
set -eu

compose_file="e2e/docker-compose.yml"

# Without a name Compose derives the project from the compose file's directory, so every
# checkout and every git worktree of this repository ran as the same project, `e2e`. A run
# begins with `down -v` on its project and its exit trap does it again, so a run started in
# one worktree deleted the containers and volumes of a run still going in another, which then
# failed part-way through for no fault of its own. The compose file publishes no host ports,
# so the project name is the only thing two runs can collide on.
#
# The default is keyed on this shell's PID, which no two scripts running at once on one host
# share, so two runs in the same checkout are kept apart as well. A name the caller sets is
# kept, and exported it reaches every `docker compose` below, the trap's included. Jobs in
# separate PID namespaces that share one Docker daemon can draw the same PID, and should
# name their projects.
: "${COMPOSE_PROJECT_NAME:=gorgany-e2e-$$}"
export COMPOSE_PROJECT_NAME
printf 'e2e harness: compose project %s\n' "$COMPOSE_PROJECT_NAME" >&2

# SQL Server runs only when asked for. Its image is amd64-only and about 1.5 GB, so it is
# behind the compose file's sqlserver profile, and E2E_REQUIRE_SQLSERVER=1 is what switches
# it on here, what the runner passes to the suite, and what makes the SQL Server cases fail
# rather than skip. The profile is exported with the project name, and for the same reason:
# every `docker compose` below has to see it, the trap's `down` included, or that `down`
# would not know the service and would leave its container running.
sqlserver_services=
if [ "${E2E_REQUIRE_SQLSERVER:-}" = "1" ]; then
  COMPOSE_PROFILES=sqlserver
  export COMPOSE_PROFILES
  sqlserver_services=mssql-live
  printf 'e2e harness: E2E_REQUIRE_SQLSERVER=1, starting SQL Server too\n' >&2
fi

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

  # The four images this run built are tagged with its project name, so with a name per run
  # they would pile up, four a run; --rmi local removes them. The pulled engine images carry
  # tags of their own and are kept.
  docker compose -f "$compose_file" down -v --remove-orphans --rmi local >/dev/null 2>&1 || true

  exit "$status"
}

trap cleanup EXIT
# dash, /bin/sh on Debian and Ubuntu and so on CI, skips the EXIT trap when a signal kills
# the script. A shared project name hid that, because the next run's opening `down` removed
# whatever an interrupted run left behind; with a name per run nothing would. Turning INT and
# TERM into exits runs the trap, so Ctrl-C tears the stack down too.
trap 'exit 130' INT
trap 'exit 143' TERM

# With the default name this finds nothing, the name being new. It is for a caller that
# reuses a name, a retried CI job say, whose earlier attempt was killed before its trap ran.
docker compose -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true

docker compose -f "$compose_file" build app-migrate app-seed app-server runner

# --wait is the other half of making these steps able to fail. Plain `up -d` reports success
# as soon as a container has been *started*, which it does even for a process that exits a
# moment later, so a database that died while initialising or a fixture app that panicked on
# boot left the harness walking on to the next step against a stack that was not running.
# --wait blocks on each service's healthcheck and exits non-zero when one of them stops.
# $sqlserver_services is unquoted on purpose: empty, it must add no argument at all.
docker compose -f "$compose_file" up -d --wait postgres postgres-live mysql-live $sqlserver_services

docker compose -f "$compose_file" run --rm app-migrate
docker compose -f "$compose_file" run --rm app-seed

docker compose -f "$compose_file" up -d --wait app-server

# The suite runs under E2E_REQUIRE_LIVE=1, set in the compose file. Every case in it gates
# itself on a dependency it can reach and skips when it cannot, so a green run used to be
# indistinguishable from a run in which nothing executed; under that variable a case that
# cannot reach its dependency fails, and a run that executed no case at all fails too.
docker compose -f "$compose_file" run --rm runner
