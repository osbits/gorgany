#!/bin/sh
# Runs the e2e suite against the production image. The exit status is the suite's.
#
#   test/e2e/run.sh                  build the image, then test it
#   APP_IMAGE=registry/app:sha ...   test an image CI already built
set -eu

cd "$(dirname "$0")/../.."
project="${COMPOSE_PROJECT_NAME:-myapp-e2e-$$}"
compose() { docker compose -p "$project" --env-file test/e2e/.env.e2e -f test/e2e/compose.yaml "$@"; }

# Read the status on the trap's first line and exit with it on the last, so the
# teardown's own `|| true` can never turn a failed run into a green one.
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    printf '\n===== e2e failed with status %s; container state and logs follow =====\n' "$status" >&2
    compose ps -a >&2 || true
    compose logs --no-color --timestamps --tail=200 >&2 || true
  fi
  # --rmi local removes the runner image this run built; the app image is kept.
  compose down -v --remove-orphans --rmi local >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT
# On Debian and Ubuntu /bin/sh is dash, which skips the EXIT trap when a signal
# kills the script. Turning INT and TERM into exits makes Ctrl-C tear it down too.
trap 'exit 130' INT
trap 'exit 143' TERM

if [ -z "${APP_IMAGE:-}" ]; then
  export APP_IMAGE=myapp:e2e
  docker build -t "$APP_IMAGE" .
fi
compose build runner

compose up -d --wait db
compose run --rm migrate
compose run --rm migrate      # again: a second run must be a no-op
compose run --rm seed
compose up -d --wait app
compose run --rm runner
