#!/usr/bin/env bash
# GitHub push helper.
# Two modes:
#  1) Version mode: `./.github/push.sh v1.2.3`
#     - Validate version, update `constants.go` FrameworkVersion to 1.2.3,
#       commit the bump if changed, create annotated tag v1.2.3, push current
#       branch to `github`, then push only that tag.
#  2) Rewrite mode (default when no args):
#     - Rewrite commit author/committer to osbits, replace old host strings,
#       push rewritten history to github/develop with tags. WARNING: rewrites history.

set -euo pipefail

AUTHOR_NAME="osbits"
AUTHOR_EMAIL="foundation@osbits.io"
COMMITTER_NAME="osbits"
COMMITTER_EMAIL="foundation@osbits.io"

# Replacement from old host to new host
OLD_BASE="git.qix.sx/gorgany/"
NEW_BASE="github.com/osbits/"

# Flow configuration (as confirmed):
BASE_BRANCH="develop"
REMOTE_NAME="github"
TEMP_BRANCH_BASE="build"

AUTO_STASH="${AUTO_STASH:-0}"
BYPASS_DIRTY="${BYPASS_DIRTY:-0}"

# Ensure we're inside a git repository
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "This script must be run inside a Git repository." >&2
  exit 1
fi

# Configure identity for commits inside containerized runs (no global config required)
# Set repo-local identity to avoid "Author identity unknown" during the replacement commit.
# Also export env vars as a safeguard so any child git process sees correct identity.
{
  git config user.name "${AUTHOR_NAME}" || true
  git config user.email "${AUTHOR_EMAIL}" || true
  # Some environments require explicit committer identity
  git config committer.name "${COMMITTER_NAME}" || true
  git config committer.email "${COMMITTER_EMAIL}" || true
  # Prefer repo config only
  git config user.useConfigOnly true || true
  # In root-in-container scenarios, mark this directory as safe to avoid ownership warnings
  git config --global --add safe.directory "$(pwd)" 2>/dev/null || true

  export GIT_AUTHOR_NAME="${AUTHOR_NAME}"
  export GIT_AUTHOR_EMAIL="${AUTHOR_EMAIL}"
  export GIT_COMMITTER_NAME="${COMMITTER_NAME}"
  export GIT_COMMITTER_EMAIL="${COMMITTER_EMAIL}"
} >/dev/null 2>&1 || true

# Common helpers
maybe_stash() {
  if ! git diff --quiet || ! git diff --cached --quiet; then
    if [ "$BYPASS_DIRTY" = "1" ]; then
      echo "Working tree is dirty but BYPASS_DIRTY=1 set; continuing without stashing."
    elif [ "$AUTO_STASH" = "1" ]; then
      echo "Working tree is dirty; AUTO_STASH=1 set. Stashing changes..."
      git stash push -u -k -m "auto-stash-$(date +%Y%m%d%H%M%S)" || true
      AUTO_STASH_STATE=1
    else
      echo "Your working tree has uncommitted changes. Please commit or stash them before running this script." >&2
      echo "Alternatively, set AUTO_STASH=1 to stash automatically, or BYPASS_DIRTY=1 to proceed at your own risk." >&2
      exit 1
    fi
  fi
}

restore_stash() {
  if [ "${AUTO_STASH_STATE:-0}" = "1" ]; then
    echo "Restoring working tree from auto-stash..."
    # Try pop first; if it fails (conflicts), attempt apply then drop
    if git stash pop -q >/dev/null 2>&1; then
      echo "Auto-stash restored."
    else
      echo "Auto-stash pop had issues; attempting apply..." >&2
      git stash apply -q || true
      git stash drop -q || true
    fi
  fi
}
trap restore_stash EXIT

normalize_version() {
  local v="$1"
  [[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  printf '%s' "$v"
}

update_framework_version() {
  local vtag="$1"      # e.g., v1.2.3
  local v_no_v="${vtag#v}"
  local file="constants.go"
  if [ ! -f "$file" ]; then
    echo "constants.go not found at $file" >&2
    exit 1
  fi
  perl -0777 -pi -e "s/const[[:space:]]+FrameworkVersion[[:space:]]*=\s*\"[^\"]*\"/const FrameworkVersion = \"${v_no_v}\"/" "$file"
  if ! git diff --quiet -- "$file"; then
    git add "$file"
    git commit -m "bump: FrameworkVersion ${v_no_v} (tag ${vtag})"
  else
    echo "FrameworkVersion already set to ${v_no_v}; no commit created."
  fi
}

VERSION_INPUT="${1:-}"
if [ -n "$VERSION_INPUT" ]; then
  # Version mode: bump + single-tag push, no history rewrite, no --tags
  if ! VERSION_TAG=$(normalize_version "$VERSION_INPUT"); then
    echo "Invalid version format: '$VERSION_INPUT'. Expected like v1.2.3" >&2
    exit 1
  fi

  # Ensure on a branch, not detached
  CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
  if [ "$CURRENT_BRANCH" = "HEAD" ]; then
    echo "Detached HEAD detected. Please checkout a branch before pushing." >&2
    exit 1
  fi

  maybe_stash
  update_framework_version "$VERSION_TAG"

  # Create annotated tag if missing
  if git rev-parse -q --verify "refs/tags/${VERSION_TAG}" >/dev/null; then
    echo "Tag ${VERSION_TAG} already exists locally."
  else
    echo "Creating tag ${VERSION_TAG}"
    git tag -a "$VERSION_TAG" -m "Release ${VERSION_TAG}"
  fi

  echo "Pushing branch '$CURRENT_BRANCH' to '${REMOTE_NAME}' ..."
  git push "${REMOTE_NAME}" "$CURRENT_BRANCH:$CURRENT_BRANCH"

  echo "Pushing tag ${VERSION_TAG} to '${REMOTE_NAME}' ..."
  git push "${REMOTE_NAME}" "$VERSION_TAG"

  echo "Done (version mode)."
  exit 0
fi

# Rewrite mode (no args): original behavior
original_branch=$(git rev-parse --abbrev-ref HEAD)

# Ensure base branch exists locally
if ! git show-ref --verify --quiet "refs/heads/${BASE_BRANCH}"; then
  echo "Base branch '${BASE_BRANCH}' not found locally. Attempting to fetch from ${REMOTE_NAME}..."
  git fetch "${REMOTE_NAME}" "${BASE_BRANCH}:${BASE_BRANCH}"
fi

# Switch to base branch if not already on it
if [ "${original_branch}" != "${BASE_BRANCH}" ]; then
  echo "Checking out base branch '${BASE_BRANCH}'"
  git checkout "${BASE_BRANCH}"
fi

# Create a unique temp branch name to avoid collisions
suffix=$(date +%Y%m%d%H%M%S)
TEMP_BRANCH="${TEMP_BRANCH_BASE}-${suffix}"

echo "Creating and switching to temporary branch: ${TEMP_BRANCH} (from ${BASE_BRANCH})"
git checkout -b "${TEMP_BRANCH}" "${BASE_BRANCH}"

# Safety backup branch (optional)
backup_branch="backup/pre-rewrite-${suffix}"
echo "Creating safety backup branch: ${backup_branch} (from ${TEMP_BRANCH})"
git branch "${backup_branch}"

# Rewrite authors/committers across the entire repo (branches + tags) as requested
# shellcheck disable=SC2016
set +e
git filter-branch -f \
  --tag-name-filter cat \
  --commit-filter '
        GIT_AUTHOR_NAME="osbits";
        GIT_AUTHOR_EMAIL="foundation@osbits.io";
        GIT_COMMITTER_NAME="osbits";
        GIT_COMMITTER_EMAIL="foundation@osbits.io";
        git commit-tree "$@";
    ' \
  -- --all
status=$?
set -e
if [ $status -ne 0 ]; then
  echo "git filter-branch failed. Aborting." >&2
  git checkout "${BASE_BRANCH}" || true
  exit 1
fi

echo "Done rewriting commit metadata and tags. Now replacing occurrences in tracked files:"
echo "  ${OLD_BASE} -> ${NEW_BASE}"

# Replace only in tracked files, avoid touching .git
if [ -z "$(git ls-files)" ]; then
  echo "No tracked files found to update."
else
  # Use Perl for portable in-place replacement across platforms (macOS/Linux)
  # Run on all tracked files safely, handling special characters via NUL separation
  # Exclude any files that are inside directories whose names start with a dot (e.g., .git, .github, .cache) at any depth.
  # Keep dotfiles themselves (like .env) eligible for replacement.
  git ls-files -z -- ':(glob)**' ':(glob,exclude).*/**' ':(glob,exclude)**/.*/**' \
    | xargs -0 perl -0777 -pi -e 's/git\.qix\.sx\/gorgany\//github\.com\/osbits\//g'

  git ls-files -z -- ':(glob)**' ':(glob,exclude).*/**' ':(glob,exclude)**/.*/**' \
    | xargs -0 perl -0777 -pi -e 's/github\.com\/osbits\/gorgany\.git/github\.com\/osbits\/gorgany/g'

  if ! git diff --quiet; then
    git add -A
    git commit -m "Replace ${OLD_BASE} with ${NEW_BASE} across repository"
    echo "Committed replacement changes."
  else
    echo "No replacements were necessary."
  fi
fi

# Push temp branch to remote develop, rewriting history and tags
echo "Force-pushing rewritten history to ${REMOTE_NAME}/develop (from ${TEMP_BRANCH}) with tags..."
git push --force-with-lease "${REMOTE_NAME}" "${TEMP_BRANCH}:develop" --tags

echo "Switching back to base branch '${BASE_BRANCH}'"
git checkout "${BASE_BRANCH}"

echo "Deleting temporary branch '${TEMP_BRANCH}' locally"
git branch -D "${TEMP_BRANCH}"

cat <<EOF
All done.
- Remote updated: ${REMOTE_NAME}/develop (force-with-lease) + tags
- Local branch restored: ${BASE_BRANCH}
- Temporary branch deleted: ${TEMP_BRANCH}
- Safety backup kept: ${backup_branch}

Optional cleanup of filter-branch backups:
  rm -rf .git/refs/original/
  git reflog expire --expire=now --all
  git gc --prune=now --aggressive
EOF
