#!/usr/bin/env bash
# Run govulncheck over one or more modules and fail on any vulnerability the
# code actually reaches, except those listed in .github/govulncheck-allow.txt.
#
#   scripts/govulncheck.sh . cmd/bosun modules/eventamqpmod ...
#
# Each module is scanned on its own with GOWORK=off, so nested modules are
# checked against the dependency versions their users get. The govulncheck
# version is pinned here and installed into a temporary directory, so it never
# enters a go.mod.
# govulncheck reports standard-library findings for the Go toolchain it runs
# on, so run it with the latest Go release, as CI does.

set -uo pipefail

GOVULNCHECK="golang.org/x/vuln/cmd/govulncheck@v1.9.0"

root="$(cd "$(dirname "$0")/.." && pwd)"
allow_file="$root/.github/govulncheck-allow.txt"

# Allowlisted IDs: the first field of every non-comment line.
allowed=""
if [[ -f "$allow_file" ]]; then
  allowed="$(sed -e 's/#.*//' "$allow_file" | awk 'NF { print $1 }')"
fi

[[ $# -gt 0 ]] || set -- .

# go run would mask govulncheck's exit code (3 = vulnerabilities found), so
# install the binary instead.
bin_dir="$(mktemp -d)"
trap 'rm -rf "$bin_dir"' EXIT
GOBIN="$bin_dir" GOWORK=off go install "$GOVULNCHECK" || exit 1
govulncheck="$bin_dir/govulncheck"

status=0
for m in "$@"; do
  echo "::group::govulncheck $m"
  out="$(cd "$root/$m" && GOWORK=off "$govulncheck" ./... 2>&1)"
  code=$?
  echo "$out"
  echo "::endgroup::"

  case $code in
    0) echo "$m: no reachable vulnerabilities" ;;
    3)
      # Text mode lists only the vulnerabilities the code reaches, each under
      # a "Vulnerability #N: GO-YYYY-NNNN" heading.
      ids="$(printf '%s\n' "$out" | sed -n -E 's/^Vulnerability #[0-9]+: (GO-[0-9]+-[0-9]+).*/\1/p' | sort -u)"
      for id in $ids; do
        if printf '%s\n' "$allowed" | grep -qx "$id"; then
          echo "$m: $id is allowlisted in .github/govulncheck-allow.txt"
        else
          echo "::error::$m: reachable vulnerability $id (https://pkg.go.dev/vuln/$id)"
          status=1
        fi
      done
      if [[ -z "$ids" ]]; then
        echo "::error::$m: govulncheck reported vulnerabilities but none could be parsed"
        status=1
      fi
      ;;
    *)
      echo "::error::$m: govulncheck failed (exit $code)"
      status=1
      ;;
  esac
done
exit $status
