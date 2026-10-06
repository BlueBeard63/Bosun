#!/usr/bin/env bash
# Release every Bosun module at one version.
#
#   scripts/release.sh v0.7.0              # dry run: checks + prints the plan
#   scripts/release.sh v0.7.0 --execute    # actually tag, commit and push
#
# The repository holds a root module plus nested modules that each require
# the root by version, so a release happens in a fixed order:
#
#   1. tag-root     tag the root module (vX.Y.Z) on HEAD and push the tag
#   2. bump         point every nested module at the new root tag
#                   (go get + tidy, update the scaffold's fallback version),
#                   test each one against the real tag, commit, push
#   3. tag-modules  tag cmd/bosun/vX.Y.Z and modules/<name>/vX.Y.Z on the
#                   bump commit and push the tags
#
# Nested modules can't require vX.Y.Z until that tag exists, which is why the
# bump commit comes after the root tag. If a step fails part-way, fix the
# cause and resume with --from-step <step>.
#
# Nothing is changed without --execute. Run it from a clean, up-to-date
# checkout of the release branch (master by default).

set -euo pipefail

ROOT_MODULE="github.com/bluebeard63/bosun"
# Every nested module, in the order they are bumped and tagged. Keep in sync
# with .github/workflows/ci.yml.
NESTED_MODULES=(
  cmd/bosun
  modules/eventamqpmod
  modules/eventnatsmod
  modules/eventredismod
  modules/stores3mod
  modules/traceotelmod
)
SCAFFOLD_FILE="cmd/bosun/internal/scaffold/scaffold.go"
STEPS=(tag-root bump tag-modules)

usage() {
  cat <<EOF
Usage: scripts/release.sh <vX.Y.Z> [options]

Options:
  --execute            perform the release (default: dry run)
  --from-step <step>   resume at: ${STEPS[*]} (default: tag-root)
  --branch <name>      branch to release from (default: master)
  --remote <name>      remote to push to (default: origin)
  --skip-tests         skip the pre-release test run (not recommended)
  --print-notes        print this version's CHANGELOG.md section and exit
  -h, --help           show this help
EOF
}

# --- arguments ---

VERSION=""
EXECUTE=0
FROM_STEP="tag-root"
BRANCH="master"
REMOTE="origin"
SKIP_TESTS=0
PRINT_NOTES=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --execute) EXECUTE=1 ;;
    --from-step) FROM_STEP="${2:?--from-step needs a value}"; shift ;;
    --branch) BRANCH="${2:?--branch needs a value}"; shift ;;
    --remote) REMOTE="${2:?--remote needs a value}"; shift ;;
    --skip-tests) SKIP_TESTS=1 ;;
    --print-notes) PRINT_NOTES=1 ;;
    -h | --help) usage; exit 0 ;;
    -*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
    *)
      [[ -z "$VERSION" ]] || { echo "unexpected argument: $1" >&2; exit 2; }
      VERSION="$1"
      ;;
  esac
  shift
done

[[ -n "$VERSION" ]] || { usage >&2; exit 2; }

# --- helpers ---

if [[ -t 1 ]]; then
  BOLD=$'\e[1m' DIM=$'\e[2m' RED=$'\e[31m' GREEN=$'\e[32m' YELLOW=$'\e[33m' RESET=$'\e[0m'
else
  BOLD="" DIM="" RED="" GREEN="" YELLOW="" RESET=""
fi

step() { printf '\n%s==> %s%s\n' "$BOLD" "$*" "$RESET"; }
info() { printf '    %s\n' "$*"; }
ok() { printf '    %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '    %s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
die() { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

# run executes a state-changing command, or only prints it in a dry run.
run() {
  if [[ $EXECUTE -eq 1 ]]; then
    printf '    %s$ %s%s\n' "$DIM" "$*" "$RESET"
    "$@"
  else
    printf '    %s[dry run] %s%s\n' "$YELLOW" "$*" "$RESET"
  fi
}

step_index() {
  local i
  for i in "${!STEPS[@]}"; do
    [[ "${STEPS[$i]}" == "$1" ]] && { echo "$i"; return; }
  done
  echo -1
}

# should_run <step>: true if <step> is at or after --from-step.
should_run() { [[ $(step_index "$1") -ge $(step_index "$FROM_STEP") ]]; }

local_tag_exists() { git rev-parse -q --verify "refs/tags/$1" >/dev/null; }
remote_tag_exists() { [[ -n "$(git ls-remote --tags "$REMOTE" "refs/tags/$1")" ]]; }

# changelog_section prints the CHANGELOG.md section for $VERSION: from its
# "## [X.Y.Z]" / "## vX.Y.Z" heading up to (not including) the next "## ".
changelog_section() {
  [[ -f CHANGELOG.md ]] || return 1
  awk -v ver="${VERSION#v}" '
    /^## / {
      if (found) exit
      h = $0
      sub(/^## \[?v?/, "", h)
      if (index(h, ver) == 1) {
        rest = substr(h, length(ver) + 1)
        if (rest == "" || rest ~ /^[] \t]/) found = 1
      }
    }
    found { print }
    END { exit !found }
  ' CHANGELOG.md
}

module_tags() {
  local m
  for m in "${NESTED_MODULES[@]}"; do echo "$m/$VERSION"; done
}

# Fetch the new root version directly from the VCS (not the module proxy),
# so a freshly pushed tag resolves immediately and private repos work.
export GOPRIVATE="${GOPRIVATE:-github.com/bluebeard63/*}"

# --- preflight (read-only; runs in dry runs too) ---

[[ $(step_index "$FROM_STEP") -ge 0 ]] || die "unknown step '$FROM_STEP' (want one of: ${STEPS[*]})"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] ||
  die "version must look like v1.2.3 or v1.2.3-rc.1, got '$VERSION'"

cd "$(git rev-parse --show-toplevel)"

if [[ $PRINT_NOTES -eq 1 ]]; then
  changelog_section || die "CHANGELOG.md has no section for $VERSION"
  exit 0
fi

command -v go >/dev/null || die "go is not installed"

if [[ $EXECUTE -eq 1 ]]; then
  step "Releasing $VERSION from $BRANCH (EXECUTE)"
else
  step "Releasing $VERSION from $BRANCH (dry run: nothing will be changed)"
fi

step "Preflight"
current_branch="$(git rev-parse --abbrev-ref HEAD)"
[[ "$current_branch" == "$BRANCH" ]] || die "on branch '$current_branch', expected '$BRANCH' (use --branch to override)"
ok "on $BRANCH"

[[ -z "$(git status --porcelain)" ]] || die "working tree is not clean; commit or stash first"
ok "working tree clean"

git fetch --quiet --tags "$REMOTE" "$BRANCH"
[[ "$(git rev-parse HEAD)" == "$(git rev-parse "$REMOTE/$BRANCH")" ]] ||
  die "HEAD is not $REMOTE/$BRANCH; pull or push first"
ok "up to date with $REMOTE/$BRANCH"

if should_run tag-root; then
  ! local_tag_exists "$VERSION" || die "tag $VERSION already exists locally (resume with --from-step bump?)"
  ! remote_tag_exists "$VERSION" || die "tag $VERSION already exists on $REMOTE (resume with --from-step bump?)"
  ok "root tag $VERSION is free"
else
  remote_tag_exists "$VERSION" || die "--from-step $FROM_STEP needs $VERSION pushed to $REMOTE first"
  ok "root tag $VERSION found on $REMOTE"
fi

for t in $(module_tags); do
  ! local_tag_exists "$t" || die "tag $t already exists locally"
  ! remote_tag_exists "$t" || die "tag $t already exists on $REMOTE"
done
ok "module tags are free: $(module_tags | tr '\n' ' ')"

if changelog_section >/dev/null; then
  ok "CHANGELOG.md has a $VERSION section"
else
  warn "CHANGELOG.md has no section for $VERSION"
fi

# --- tests ---

if [[ $SKIP_TESTS -eq 1 ]]; then
  step "Tests (skipped by --skip-tests)"
elif ! should_run tag-root; then
  step "Tests (skipped when resuming; the bump step tests each module)"
else
  step "Tests"
  info "root module"
  GOWORK=off go vet ./...
  GOWORK=off go test -race -count=1 ./... >/dev/null || die "root module tests failed"
  ok "root module"

  # Test nested modules against this checkout, as CI does.
  work_dir="$(mktemp -d)"
  trap 'rm -rf "$work_dir"' EXIT
  (cd "$work_dir" && go work init "$OLDPWD" $(printf "$OLDPWD/%s " "${NESTED_MODULES[@]}"))
  for m in "${NESTED_MODULES[@]}"; do
    (cd "$m" && GOWORK="$work_dir/go.work" go vet ./... &&
      GOWORK="$work_dir/go.work" go test -race -count=1 ./... >/dev/null) || die "$m tests failed"
    ok "$m (against this checkout)"
  done
fi

# --- 1. tag-root ---

if should_run tag-root; then
  step "1/3 Tag root module $VERSION"
  run git tag -a "$VERSION" -m "bosun $VERSION"
  run git push "$REMOTE" "refs/tags/$VERSION"
fi

# --- 2. bump ---

if should_run bump; then
  step "2/3 Bump nested modules to $ROOT_MODULE@$VERSION"
  if [[ $EXECUTE -eq 1 ]]; then
    for m in "${NESTED_MODULES[@]}"; do
      info "$m"
      attempt=1
      until (cd "$m" && GOWORK=off GOFLAGS=-mod=mod go get "$ROOT_MODULE@$VERSION" >/dev/null 2>&1); do
        [[ $attempt -lt 6 ]] || die "$m: go get $ROOT_MODULE@$VERSION kept failing; is the tag pushed?"
        warn "$m: $VERSION not resolvable yet, retrying in 10s ($attempt/5)"
        sleep 10
        attempt=$((attempt + 1))
      done
      (cd "$m" && GOWORK=off go mod tidy)
      (cd "$m" && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./... >/dev/null) ||
        die "$m fails against the released $VERSION; fix, then resume with --from-step bump"
      ok "$m requires $VERSION and passes its tests"
    done

    sed -i.bak -E "s/^(const fallbackBosunVersion = )\"[^\"]*\"/\1\"$VERSION\"/" "$SCAFFOLD_FILE"
    rm -f "$SCAFFOLD_FILE.bak"
    grep -q "fallbackBosunVersion = \"$VERSION\"" "$SCAFFOLD_FILE" || die "could not update $SCAFFOLD_FILE"
    ok "scaffold fallbackBosunVersion = $VERSION"

    if [[ -z "$(git status --porcelain)" ]]; then
      warn "nothing to commit (modules already at $VERSION?)"
    else
      files=("$SCAFFOLD_FILE")
      for m in "${NESTED_MODULES[@]}"; do files+=("$m/go.mod" "$m/go.sum"); done
      run git add -- "${files[@]}"
      [[ -z "$(git status --porcelain --untracked-files=no | grep -v '^M  ')" ]] ||
        die "unexpected changes besides go.mod/go.sum/scaffold; inspect with git status"
      run git commit -m "chore: bump nested modules to bosun $VERSION"
      run git push "$REMOTE" "$BRANCH"
    fi
  else
    for m in "${NESTED_MODULES[@]}"; do
      run "(cd $m && go get $ROOT_MODULE@$VERSION && go mod tidy && go test ./...)"
    done
    run "update $SCAFFOLD_FILE: fallbackBosunVersion = \"$VERSION\""
    run git add -- "$SCAFFOLD_FILE" "<nested go.mod/go.sum>"
    run git commit -m "chore: bump nested modules to bosun $VERSION"
    run git push "$REMOTE" "$BRANCH"
  fi
fi

# --- 3. tag-modules ---

if should_run tag-modules; then
  step "3/3 Tag nested modules"
  if [[ $EXECUTE -eq 1 && "$FROM_STEP" == "tag-modules" ]]; then
    [[ "$(git rev-parse HEAD)" == "$(git rev-parse "$REMOTE/$BRANCH")" ]] ||
      die "push the bump commit before tagging modules"
  fi
  refs=()
  for t in $(module_tags); do
    run git tag -a "$t" -m "bosun $t"
    refs+=("refs/tags/$t")
  done
  run git push "$REMOTE" "${refs[@]}"
fi

# --- done ---

step "Done"
if [[ $EXECUTE -eq 1 ]]; then
  ok "released $VERSION: root plus ${#NESTED_MODULES[@]} nested modules"
  info "Verify:  GOPRIVATE=$GOPRIVATE go install $ROOT_MODULE/cmd/bosun@$VERSION && bosun --version"
  info "Release: gh release create $VERSION --title $VERSION --notes \"\$(scripts/release.sh $VERSION --print-notes)\""
else
  ok "dry run complete; re-run with --execute to release"
fi
