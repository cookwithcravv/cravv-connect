#!/usr/bin/env bash
# Deploys relay-cf to your own Cloudflare account, or updates it. Safe to run
# again: every step checks what already exists.
# Usage (from the repo root): relay-cf/scripts/deploy.sh
#
# It installs the npm packages, logs wrangler in if needed, creates the R2
# bucket and its lifecycle rule, generates the admin token into a private
# file, deploys the Worker with PUBLIC_ORIGIN pinned to its workers.dev URL,
# hands the token to Cloudflare, waits for /v1/health, and prints the
# `cravv-connect setup` command for your first machine.
#
# Env overrides: TOKEN_FILE (default ~/.cravv-relay-admin-token), RELAY_URL
# (the URL clients use, when it is not the workers.dev one), HEALTH_TRIES
# (default 60, 5 s apart).
set -euo pipefail

RELAY_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TOKEN_FILE="${TOKEN_FILE:-$HOME/.cravv-relay-admin-token}"
URL_FILE="$RELAY_DIR/.relay-url"
BUCKET="cravv-relay-blobs"
HEALTH_TRIES="${HEALTH_TRIES:-60}"

say() { printf '\n==> %s\n' "$*"; }
die() { printf '\nerror: %s\n' "$*" >&2; exit 1; }

cd "$RELAY_DIR"

say "Checking Node.js"
command -v node >/dev/null 2>&1 || die "Node.js 22 or newer is needed: https://nodejs.org"
major="$(node --version | sed -E 's/^v([0-9]+).*/\1/')"
[ "$major" -ge 22 ] 2>/dev/null || die "Node.js 22 or newer is needed (found $(node --version))"
if [ ! -x node_modules/.bin/wrangler ] || [ package-lock.json -nt node_modules/.package-lock.json ]; then
  npm ci --no-audit --no-fund
fi

say "Checking the Cloudflare login"
if ! npx wrangler whoami --json >/dev/null 2>&1; then
  npx wrangler login
  npx wrangler whoami --json >/dev/null 2>&1 || die "wrangler is not logged in"
fi

say "R2 bucket $BUCKET"
if ! npx wrangler r2 bucket info "$BUCKET" >/dev/null 2>&1; then
  npx wrangler r2 bucket create "$BUCKET" ||
    die "could not create the R2 bucket. If R2 is not enabled yet: in the Cloudflare dashboard open R2 Object Storage and accept the plan (the free tier is enough), then run this again."
fi
# A backstop only: the relay deletes blobs itself after 7 days.
rules="$(npx wrangler r2 bucket lifecycle list "$BUCKET" 2>/dev/null || true)"
if ! printf '%s\n' "$rules" | grep -q expire-blobs; then
  npx wrangler r2 bucket lifecycle add "$BUCKET" expire-blobs blobs/ --expire-days 15 --force
fi

say "Admin token"
if [ -s "$TOKEN_FILE" ]; then
  echo "Using the existing token in $TOKEN_FILE"
else
  (
    umask 077
    if command -v openssl >/dev/null 2>&1; then
      openssl rand -hex 32 >"$TOKEN_FILE"
    else
      od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$TOKEN_FILE"
      echo >>"$TOKEN_FILE"
    fi
  )
  echo "Wrote a new token to $TOKEN_FILE (it never appears on screen)"
fi
chmod 600 "$TOKEN_FILE"

# deploy runs `wrangler deploy`, pinning PUBLIC_ORIGIN when $1 is set, and
# prints the workers.dev URL wrangler reports (empty if none).
deploy() {
  local out
  if [ -n "$1" ]; then
    out="$(npx wrangler deploy --var "PUBLIC_ORIGIN:$1" 2>&1 | tee /dev/stderr)" || deploy_failed "$out"
  else
    out="$(npx wrangler deploy 2>&1 | tee /dev/stderr)" || deploy_failed "$out"
  fi
  printf '%s\n' "$out" | grep -Eo 'https://[A-Za-z0-9.-]+\.workers\.dev' | head -n 1 || true
}

deploy_failed() {
  case "$1" in
    *workers.dev\ subdomain*|*10063*)
      die "your account has no workers.dev subdomain yet. In the Cloudflare dashboard open Compute (Workers), then Workers & Pages; opening it creates the subdomain. Then run this again." ;;
  esac
  die "wrangler deploy failed (see above)"
}

say "Deploying the relay"
url="${RELAY_URL:-}"
if [ -z "$url" ] && [ -s "$URL_FILE" ]; then url="$(cat "$URL_FILE")"; fi
url="${url%/}"
got="$(deploy "$url")"
if [ -z "${RELAY_URL:-}" ] && [ -n "$got" ] && [ "$got" != "$url" ]; then
  if [ -n "$url" ]; then
    echo "The relay URL changed from $url to $got: machines set up on the old URL must run" \
      "cravv-connect setup --reset --relay $got and pair again."
  fi
  url="$got"
  say "Pinning PUBLIC_ORIGIN to $url"
  deploy "$url" >/dev/null
fi
[ -n "$url" ] || die "could not tell the relay URL from wrangler's output; run again with RELAY_URL=https://..."
printf '%s\n' "$url" >"$URL_FILE"

say "Handing the admin token to Cloudflare"
npx wrangler secret put ADMIN_TOKEN <"$TOKEN_FILE"

say "Waiting for $url/v1/health"
ok=""
for i in $(seq 1 "$HEALTH_TRIES"); do
  health="$(curl -fsS "$url/v1/health" 2>/dev/null || true)"
  case "$health" in *'"ok":true'*) ok=1; break ;; esac
  [ "$i" -eq "$HEALTH_TRIES" ] || sleep 5
done
[ -n "$ok" ] || die "the relay is deployed but $url/v1/health does not answer yet. A new workers.dev subdomain can take a few minutes; check it with: curl $url/v1/health"

cat <<EOF

Your relay is ready: $url

Set up your first machine with it:

  cravv-connect setup --relay $url --relay-token - < $TOKEN_FILE

Every other machine joins with a code from \`cravv-connect pair\`, so the
token is needed only on the first one. Keep $TOKEN_FILE private: it lets
anyone register new machines on your relay. To update the relay later, pull
the repository and run this script again.
EOF
