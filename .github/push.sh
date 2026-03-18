#!/usr/bin/env bash
# Build and run a Docker container that pushes to GitHub using the id_osbits key
# Canonical entrypoint: use this script instead of running github-push.sh directly on host.

set -euo pipefail

IMAGE_NAME="gorgany/github-push:latest"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$SCRIPT_DIR/.."
KEY_FILE_DEFAULT="$REPO_DIR/id_osbits"

# Allow overriding the key path
KEY_FILE="${ID_OSBITS_KEY_PATH:-$KEY_FILE_DEFAULT}"

# Cleanup built image on exit unless KEEP_IMAGE=1
cleanup() {
  if [ "${KEEP_IMAGE:-0}" != "1" ]; then
    echo "Cleaning up Docker image: $IMAGE_NAME"
    docker rmi -f "$IMAGE_NAME" >/dev/null 2>&1 || true
    if [ "${PRUNE_DANGLING:-0}" = "1" ]; then
      docker image prune -f >/dev/null 2>&1 || true
    fi
  else
    echo "KEEP_IMAGE=1 set; skipping image removal."
  fi
}
trap cleanup EXIT

if [ ! -f "$KEY_FILE" ]; then
  echo "SSH private key not found at: $KEY_FILE" >&2
  echo "Place your key at $KEY_FILE or set ID_OSBITS_KEY_PATH=/path/to/id_osbits" >&2
  exit 1
fi

# Try to detect owner/repo from the 'github' remote
GITHUB_REPO_ENV="${GITHUB_REPO:-}"
if [ -z "$GITHUB_REPO_ENV" ]; then
  if git -C "$REPO_DIR" remote get-url github >/dev/null 2>&1; then
    url=$(git -C "$REPO_DIR" remote get-url github)
    if [[ "$url" =~ github\.com[:/]+([^/]+/[^/.]+) ]]; then
      GITHUB_REPO_ENV="${BASH_REMATCH[1]}"
    fi
  fi
fi

# Build the Docker image (always rebuild to ensure latest entrypoint/script)
echo "Building Docker image: $IMAGE_NAME"
docker build -f "$SCRIPT_DIR/Dockerfile" -t "$IMAGE_NAME" "$SCRIPT_DIR"

# On macOS, Docker Desktop shares volumes; ensure absolute paths
WORKDIR_MNT="$REPO_DIR"
KEY_MNT="$KEY_FILE"

# Prepare docker run arguments
RUN_ARGS=(
  --rm
  -v "$WORKDIR_MNT:/workspace"
  -v "$KEY_MNT:/ssh/id_osbits:ro"
  -e VERIFY_SSH="${VERIFY_SSH:-1}"
  -e AUTO_STASH="${AUTO_STASH:-}"
  -e BYPASS_DIRTY="${BYPASS_DIRTY:-}"
)

if [ -n "$GITHUB_REPO_ENV" ]; then
  RUN_ARGS+=( -e "GITHUB_REPO=$GITHUB_REPO_ENV" )
fi

# Default command to run inside the container: explicitly run the internal workflow
# Always invoke the internal script and pass through any arguments (e.g., version tag)
if [ "$#" -gt 0 ]; then
  CMD=(./.github/github.sh "$@")
else
  CMD=(./.github/github.sh)
fi

echo "Running container to perform GitHub push via Docker..."
docker run "${RUN_ARGS[@]}" "$IMAGE_NAME" "${CMD[@]}"

echo "Done."
