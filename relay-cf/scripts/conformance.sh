#!/usr/bin/env bash
# Runs the Go conformance suite against relay-cf under a local `wrangler dev`.
# Usage (from the repo root): make conformance-cf
# Env overrides: PORT (default 8787), ADMIN_TOKEN (default conformance-admin-token),
# CONFORMANCE_CMD (default "go run ./cmd/cravv-conformance"), DISABLE_RATE_LIMITS (default 0; set 1 to relax both limits).
# Extra arguments are passed to the suite, for example --slow or -test.run 'Conformance/rooms'.
# The admin token reaches wrangler through an env file and the suite through
# CRAVV_CONFORMANCE_ADMIN_TOKEN, so it never appears on a command line (ps output).
set -euo pipefail

RELAY_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$RELAY_DIR/.." && pwd)"
PORT="${PORT:-8787}"
ADMIN_TOKEN="${ADMIN_TOKEN:-conformance-admin-token}"
CONFORMANCE_CMD="${CONFORMANCE_CMD:-go run ./cmd/cravv-conformance}"
URL="http://127.0.0.1:${PORT}"
STATE_DIR="$(mktemp -d)"
LOG="${STATE_DIR}/wrangler.log"

WRANGLER_PID=""
cleanup() {
  if [ -n "$WRANGLER_PID" ]; then
    kill "$WRANGLER_PID" 2>/dev/null || true
    wait "$WRANGLER_PID" 2>/dev/null || true
  fi
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

SECRETS="${STATE_DIR}/secrets.env"
(umask 077 && printf 'ADMIN_TOKEN=%s\n' "$ADMIN_TOKEN" >"$SECRETS")

cd "$RELAY_DIR"
npx wrangler dev --ip 127.0.0.1 --port "$PORT" --persist-to "$STATE_DIR/state" \
  --show-interactive-dev-session=false --env-file "$SECRETS" --var "PUBLIC_ORIGIN:${URL}" --var "DISABLE_RATE_LIMITS:${DISABLE_RATE_LIMITS:-0}" \
  >"$LOG" 2>&1 &
WRANGLER_PID=$!

for _ in $(seq 1 60); do
  if curl -sf "$URL/v1/health" >/dev/null; then break; fi
  if ! kill -0 "$WRANGLER_PID" 2>/dev/null; then cat "$LOG"; exit 1; fi
  sleep 1
done
curl -sf "$URL/v1/health" >/dev/null || { echo "wrangler dev did not start"; cat "$LOG"; exit 1; }

cd "$REPO_ROOT"
export CRAVV_CONFORMANCE_ADMIN_TOKEN="$ADMIN_TOKEN"
# shellcheck disable=SC2086
$CONFORMANCE_CMD --relay "$URL" "$@"
