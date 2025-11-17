#!/usr/bin/env bash
set -euo pipefail

# Ensure SSH key is present
SRC_KEY_PATH="/ssh/id_osbits"
if [ ! -f "$SRC_KEY_PATH" ]; then
  echo "Missing SSH key at $SRC_KEY_PATH. Mount your private key: -v $(pwd)/id_osbits:/ssh/id_osbits:ro" >&2
  exit 1
fi

# Copy the key to a writable location if needed (read-only mounts prevent chmod)
EFFECTIVE_KEY_PATH="/tmp/id_osbits"
mkdir -p /tmp
# Try to detect if src is writable; if not, copy
if ! chmod 600 "$SRC_KEY_PATH" >/dev/null 2>&1; then
  cp "$SRC_KEY_PATH" "$EFFECTIVE_KEY_PATH"
  chmod 600 "$EFFECTIVE_KEY_PATH"
else
  # If chmod worked on source, use it directly
  EFFECTIVE_KEY_PATH="$SRC_KEY_PATH"
fi

mkdir -p /root/.ssh

# Prepare known_hosts to avoid prompts
if ! ssh-keygen -F github.com >/dev/null 2>&1; then
  ssh-keyscan -H github.com >> /root/.ssh/known_hosts 2>/dev/null || true
fi

# Export GIT_SSH_COMMAND if not already set (Dockerfile sets a sane default)
: "${GIT_SSH_COMMAND:=ssh -i $EFFECTIVE_KEY_PATH -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new}"
# Ensure it always points to the effective key path
GIT_SSH_COMMAND="ssh -i $EFFECTIVE_KEY_PATH -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
export GIT_SSH_COMMAND

echo "Using GIT_SSH_COMMAND: $GIT_SSH_COMMAND"

cd /workspace

# Optional: verify access (won't fail the whole run to avoid blocking CI if GitHub blocks probing)
if [ "${VERIFY_SSH:-1}" = "1" ]; then
  if ! ssh -o BatchMode=yes -i "$EFFECTIVE_KEY_PATH" -T git@github.com 2>&1 | grep -qi "successfully authenticated\|Hi "; then
    echo "Warning: Could not confirm GitHub auth non-interactively. Continuing..." >&2
  else
    echo "GitHub SSH authentication looks OK."
  fi
fi

# Ensure remote uses SSH (best effort)
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  if git remote get-url github >/dev/null 2>&1; then
    url=$(git remote get-url github)
    if [[ "$url" != git@github.com:* && "$url" != ssh://git@github.com/* ]]; then
      echo "Updating 'github' remote to use SSH URL..."
      # Try to convert HTTPS to SSH
      # If module path is available in go.mod or .git/config, user should ensure correctness.
      owner_repo=${GITHUB_REPO:-}
      if [ -z "$owner_repo" ]; then
        # Try to infer from existing URL
        if [[ "$url" =~ github\.com[:/]+([^/]+/[^/.]+) ]]; then
          owner_repo="${BASH_REMATCH[1]}"
        fi
      fi
      if [ -n "$owner_repo" ]; then
        git remote set-url github "git@github.com:${owner_repo}.git" || true
      else
        echo "Could not infer repository path; leaving remote URL unchanged: $url" >&2
      fi
    fi
  fi
fi

# Default: do not auto-run legacy scripts. Require an explicit command.
# This ensures the host-side Docker wrapper (docker/push.sh) controls what runs.
if [ "$#" -eq 0 ]; then
  echo "No command provided. Use the host wrapper: ./.github/push.sh (preferred)." >&2
  echo "Alternatively, pass an explicit command, e.g.: docker run ... bash -lc './docker/github.sh'" >&2
  exit 2
else
  exec "$@"
fi
