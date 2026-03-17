#!/bin/sh
set -eu

compose_file="e2e/docker-compose.yml"

cleanup() {
  docker compose -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true
}

trap cleanup EXIT

docker compose -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true
docker compose -f "$compose_file" build app-migrate app-seed app-server runner
docker compose -f "$compose_file" up -d --build postgres
docker compose -f "$compose_file" run --rm app-migrate
docker compose -f "$compose_file" run --rm app-seed
docker compose -f "$compose_file" up -d app-server
docker compose -f "$compose_file" run --rm runner
