#!/usr/bin/env bash
# Rewrite commit author/committer to osbits, replace occurrences in files,
# and perform the workflow on a temporary branch which is then force-pushed to github/develop.
# WARNING: This rewrites history. Ensure you have backups and understand the implications.

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

# Ensure we're inside a git repository
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "This script must be run inside a Git repository." >&2
  exit 1
fi

# Ensure working tree is clean to avoid accidental loss
if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "Your working tree has uncommitted changes. Please commit or stash them before running this script." >&2
  exit 1
fi

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
  git ls-files -z | xargs -0 perl -0777 -pi -e 's/git\.qix\.sx\/gorgany\//github\.com\/osbits\//g'

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
