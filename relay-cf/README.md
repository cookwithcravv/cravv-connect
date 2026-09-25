# relay-cf: cravv-connect relay on Cloudflare

This is the production relay for cravv-connect. It speaks `relay-v1` (see `../protocol/relay-v1.md`)
and nothing Cloudflare-specific leaks into the protocol. It only ever sees ciphertext and routing metadata.

## Layout

| File | Responsibility |
|---|---|
| `src/index.ts` | Worker entry: routes `/v1/connect`, `/v1/pair/{nameplate}`, `/v1/blobs/...`, `/v1/health`; per-IP rate limit |
| `src/protocol.ts` | relay-v1 constants, frame parsing, error and res frames |
| `src/crypto.ts` | base64, base32, SHA-256, Ed25519 verify, auth and HTTP signing strings |
| `src/limits.ts` | limit values and their optional env overrides |
| `src/registry.ts` | `Registry` Durable Object: members, single-use invites, blob quota |
| `src/mailbox.ts` | `Mailbox` Durable Object (one per mailbox id): handshake, queue, allow-list, delivery |
| `src/room.ts` | `Room` Durable Object (one per nameplate): pairing relay, one joiner, 10 minute life |
| `src/blobmeta.ts` | `BlobMeta` Durable Object (one per blob): ACL, expiry, cleanup |
| `src/blobs.ts` | signed HTTP handlers for `/v1/blobs`, chunk bytes in R2 |

All Durable Objects are SQLite-backed. Mailboxes use the WebSocket Hibernation API, so an idle
connection costs nothing.

## Develop and test

```sh
cd relay-cf
npm ci
npm run typecheck
npm test            # Vitest inside workerd via @cloudflare/vitest-pool-workers; no account needed
cp .dev.vars.example .dev.vars
npm run dev         # wrangler dev on http://127.0.0.1:8787
```

## Deploy

```sh
cd relay-cf
npm ci
npx wrangler login
npx wrangler r2 bucket create cravv-relay-blobs
npx wrangler secret put ADMIN_TOKEN      # paste a long random value, e.g. from: openssl rand -hex 32
npx wrangler deploy
```

`wrangler deploy` prints the Worker URL (for example `https://cravv-relay.<account>.workers.dev`).
Pin that URL as the relay origin that clients sign during auth, then deploy again:

```sh
npx wrangler deploy --var PUBLIC_ORIGIN:https://cravv-relay.<account>.workers.dev
```

(or add `[vars] PUBLIC_ORIGIN = "..."` to `wrangler.toml`). Without it the relay falls back to the
origin of each request URL. Use that URL as the relay for the first machine:

```sh
cravv-connect init --relay https://cravv-relay.<account>.workers.dev --relay-token <ADMIN_TOKEN>
```

Every other machine joins with an invite that arrives inside the encrypted pairing exchange, so the
admin token is needed only once.

## Conformance against wrangler dev

From the repository root:

```sh
make conformance-cf
```

The target runs `relay-cf/scripts/conformance.sh`, which starts `wrangler dev` on
`http://127.0.0.1:8787` with a throwaway state directory, an admin token of
`conformance-admin-token`, and `PUBLIC_ORIGIN=http://127.0.0.1:8787`, waits for
`/v1/health`, then runs:

```sh
go run ./cmd/cravv-conformance --relay http://127.0.0.1:8787 --admin-token conformance-admin-token
```

Extra arguments go to the suite, so `relay-cf/scripts/conformance.sh --slow` also fills a
mailbox to its 10000-frame and 50 MB caps. TTL cases are skipped automatically against an
external relay; the relay-cf Vitest suite covers them with `runInDurableObject` and `runDurableObjectAlarm`. Overrides: `PORT`, `ADMIN_TOKEN`,
`CONFORMANCE_CMD` (the command that receives `--relay` and `--admin-token`), and
`DISABLE_RATE_LIMITS=1` to relax both rate limits.

## Limits and overrides

Defaults match the cravv-connect spec: 256 KiB frames, 50 MB or 10000 frames per mailbox queue,
7 day queue TTL, 10 minute rooms and invites, 16 buffered room messages, 100 MiB blobs in
1 MiB chunks, 7 day blob TTL, 2 GiB of live blobs per member, 1000 requests per live mailbox
connection refilled at 200 per second, and 600 connects or blob requests per IP per minute. For local testing you can lower them in `.dev.vars` with
`QUEUE_MAX_FRAMES`, `QUEUE_MAX_BYTES`, `QUEUE_TTL_SECONDS`, `ROOM_TTL_SECONDS`,
`INVITE_TTL_SECONDS`, `BLOB_TTL_SECONDS`, `BLOB_QUOTA_BYTES`, `REQUEST_BURST`,
`REQUEST_RATE_PER_SECOND`, and `DISABLE_RATE_LIMITS=1`. Leave them unset in production.

## Protocol notes specific to this relay

- `GET /v1/connect` must carry `?ik=<base64 identity key>`. It only picks the mailbox Durable
  Object; the relay still requires the signed challenge from that same key, and a mismatch fails
  with `auth_failed`.
- The auth origin is `PUBLIC_ORIGIN` when set, otherwise `new URL(request.url).origin`. Either
  way it must equal what the Go client computes from the relay URL it was given
  (`https://host` in production, `http://127.0.0.1:8787` locally).
