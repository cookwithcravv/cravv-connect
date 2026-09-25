#!/usr/bin/env bash
# Runs the Go conformance suite against relay-cf under a local `wrangler dev`.
# Usage (from the repo root): make conformance-cf
# Env overrides: PORT (default 8787), ADMIN_TOKEN (default conformance-admin-token),
# CONFORMANCE_CMD (default "go run ./cmd/cravv-conformance"), DISABLE_RATE_LIMITS (default 0; set 1 to relax both limits).
# Extra arguments are passed to the suite, for example --slow or -test.run 'Conformance/rooms'.
set -euo pipefail

RELAY_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$RELAY_DIR/.." && pwd)"
PORT="${PORT:-8787}"
ADMIN_TOKEN="${ADMIN_TOKEN:-conformance-admin-token}"
CONFORMANCE_CMD="${CONFORMANCE_CMD:-go run ./cmd/cravv-conformance}"
URL="http://127.0.0.1:${PORT}"
STATE_DIR="$(mktemp -d)"
LOG="${STATE_DIR}/wrangler.log"

cd "$RELAY_DIR"
npx wrangler dev --ip 127.0.0.1 --port "$PORT" --persist-to "$STATE_DIR/state" \
  --show-interactive-dev-session=false --var "ADMIN_TOKEN:${ADMIN_TOKEN}" --var "PUBLIC_ORIGIN:${URL}" --var "DISABLE_RATE_LIMITS:${DISABLE_RATE_LIMITS:-0}" \
  >"$LOG" 2>&1 &
WRANGLER_PID=$!
cleanup() {
  kill "$WRANGLER_PID" 2>/dev/null || true
  wait "$WRANGLER_PID" 2>/dev/null || true
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

for _ in $(seq 1 60); do
  if curl -sf "$URL/v1/health" >/dev/null; then break; fi
  if ! kill -0 "$WRANGLER_PID" 2>/dev/null; then cat "$LOG"; exit 1; fi
  sleep 1
done
curl -sf "$URL/v1/health" >/dev/null || { echo "wrangler dev did not start"; cat "$LOG"; exit 1; }

cd "$REPO_ROOT"
# shellcheck disable=SC2086
$CONFORMANCE_CMD --relay "$URL" --admin-token "$ADMIN_TOKEN" "$@"
