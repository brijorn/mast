#!/usr/bin/env bash
set -euo pipefail

# Runs on the development Mac. Only a clean commit already present on GitHub is
# eligible; BMO then independently verifies that branch/commit pair and builds.
repo=$(cd "$(dirname "$0")/.." && pwd)
host=${MAST_DEPLOY_HOST:-brijorn@bmo}

cd "$repo"
if [[ -n "$(git status --porcelain)" ]]; then
  echo "Commit or remove local changes before deploying Mast." >&2
  git status --short >&2
  exit 1
fi

branch=$(git symbolic-ref --quiet --short HEAD) || {
  echo "Deploy from a named branch, not a detached checkout." >&2
  exit 1
}
commit=$(git rev-parse HEAD)
git fetch origin --prune
remote_commit=$(git rev-parse "origin/$branch")
if [[ "$commit" != "$remote_commit" ]]; then
  echo "Local $branch is not the exact GitHub commit. Push it before deploying." >&2
  echo "local:  $commit" >&2
  echo "remote: $remote_commit" >&2
  exit 1
fi

printf 'Deploying Mast %s (%s) to %s\n' "$branch" "$commit" "$host"
ssh "$host" "~/Documents/mast/scripts/deploy-production.sh '$branch' '$commit'"
