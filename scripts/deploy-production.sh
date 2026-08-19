#!/usr/bin/env bash
set -euo pipefail

# Runs on the Mast host. The caller supplies the exact GitHub branch and commit
# so a deploy can never silently install whatever happened to be checked out.
branch=${1:?usage: deploy-production.sh <branch> <commit>}
expected_commit=${2:?usage: deploy-production.sh <branch> <commit>}

case "$branch" in
  *[!A-Za-z0-9._/-]*|"") echo "Invalid branch name: $branch" >&2; exit 2 ;;
esac
case "$expected_commit" in
  *[!0-9a-f]*|"") echo "Invalid commit: $expected_commit" >&2; exit 2 ;;
esac

repo=${MAST_DEPLOY_REPO:-"$HOME/Documents/mast"}
install_dir=${MAST_INSTALL_DIR:-"$HOME/.mast/bin"}
helper_dir=${MAST_HELPER_DIR:-"$HOME/.local/bin"}
health_url=${MAST_HEALTH_URL:-"http://127.0.0.1:6271/api/nodes"}
lock_file=${MAST_DEPLOY_LOCK:-"$HOME/.mast/deploy.lock"}

exec 9>"$lock_file"
if ! flock -n 9; then
  echo "Another Mast deployment is already running." >&2
  exit 1
fi

cd "$repo"
if [[ -n "$(git status --porcelain)" ]]; then
  echo "Refusing to deploy from a dirty Mast checkout: $repo" >&2
  git status --short >&2
  exit 1
fi

git fetch origin --prune
remote_commit=$(git rev-parse "origin/$branch")
if [[ "$remote_commit" != "$expected_commit" ]]; then
  echo "GitHub branch moved: expected $expected_commit, found $remote_commit" >&2
  exit 1
fi

# A detached production checkout makes the deployed commit the state, instead
# of letting an old local branch or an accidental merge decide production.
git switch --detach "$expected_commit"

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/mast-deploy.XXXXXX")
trap 'rm -rf "$build_dir"' EXIT
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
short_commit=${expected_commit:0:12}

go test ./...
go build -trimpath \
  -ldflags "-s -w -X github.com/brijorn/mast/internal/version.Version=dev-$short_commit -X github.com/brijorn/mast/internal/version.Commit=$expected_commit -X github.com/brijorn/mast/internal/version.Date=$build_date" \
  -o "$build_dir/mast" ./cmd/mast
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w" \
  -o "$build_dir/winprompt.exe" ./cmd/winprompt

mkdir -p "$install_dir"
if [[ -x "$install_dir/mast" ]]; then
  cp -p "$install_dir/mast" "$install_dir/mast.previous"
fi
install -m 0755 "$build_dir/mast" "$install_dir/mast.next"
mv -f "$install_dir/mast.next" "$install_dir/mast"

mkdir -p "$helper_dir"
install -m 0755 scripts/winerun "$helper_dir/winerun.next"
mv -f "$helper_dir/winerun.next" "$helper_dir/winerun"
install -m 0644 "$build_dir/winprompt.exe" "$helper_dir/winprompt.exe.next"
mv -f "$helper_dir/winprompt.exe.next" "$helper_dir/winprompt.exe"

if ! systemctl --user restart mast.service; then
  echo "Mast did not restart; restoring the previous binary." >&2
  if [[ -x "$install_dir/mast.previous" ]]; then
    mv -f "$install_dir/mast.previous" "$install_dir/mast"
    systemctl --user restart mast.service
  fi
  exit 1
fi

healthy=false
for _ in {1..30}; do
  if response=$(curl --fail --silent --show-error "$health_url" 2>/dev/null) && \
     grep -Fq "$expected_commit" <<<"$response"; then
    healthy=true
    break
  fi
  sleep 1
done

if [[ "$healthy" != true ]]; then
  echo "Mast did not report commit $expected_commit; restoring the previous binary." >&2
  systemctl --user stop mast.service || true
  if [[ -x "$install_dir/mast.previous" ]]; then
    mv -f "$install_dir/mast.previous" "$install_dir/mast"
    systemctl --user start mast.service
  fi
  exit 1
fi

echo "Mast deployed successfully."
"$install_dir/mast" version --verbose
systemctl --user --no-pager --full status mast.service | sed -n '1,12p'
