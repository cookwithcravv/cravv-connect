#!/bin/sh
# Prints the license texts of every module linked into the released binaries
# (cravv-connect, cravv-relay, cravv-conformance, for macOS and Linux). The
# release archives ship the output as THIRD_PARTY_LICENSES.
# Usage (from the repo root): scripts/third_party_licenses.sh > THIRD_PARTY_LICENSES
set -eu

mods="$(
  for os in darwin linux; do
    GOOS="$os" go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' \
      ./cmd/cravv-connect ./cmd/cravv-relay ./cmd/cravv-conformance
  done | sort -u
)"

echo "cravv-connect includes the following third-party Go modules. Their license"
echo "texts follow, one section per module."
printf '%s\n' "$mods" | while read -r path version dir; do
  echo
  echo "================================================================================"
  echo "$path $version"
  echo "================================================================================"
  found=""
  for f in $(ls "$dir" | grep -i -E '^(licen[cs]e|copying|notice|patents)' | sort); do
    [ -f "$dir/$f" ] || continue
    found=1
    echo
    echo "--- $f ---"
    echo
    cat "$dir/$f"
  done
  [ -n "$found" ] || { echo "no license file found for $path" >&2; exit 1; }
done
