# relay-cf: cravv-connect relay on Cloudflare

This is the production relay for cravv-connect. It speaks `relay-v1` (see `../protocol/relay-v1.md`)
and nothing Cloudflare-specific leaks into the protocol. It only ever sees ciphertext and routing metadata.

## Layout

| File | Responsibility |
|---|---|
| `src/index.ts` | Worker entry: routes `/v1/connect`, `/v1/pair/{nameplate}`, `/v1/blobs/...`, `/v1/health`; per-IP rate limit |
| `src/protocol.ts` | relay-v1 constants, frame parsing, error and res frames |
| `src/http.ts` | shared upgrade check, JSON error responses, and error-frame rejection of upgrades |
| `src/crypto.ts` | base64, base32, SHA-256, Ed25519 verify, auth and HTTP signing strings |
| `src/limits.ts` | limit values and their optional env overrides |
| `src/registry.ts` | `Registry` Durable Object: members, single-use invites, relay-wide blob storage total |
| `src/mailbox.ts` | `Mailbox` Durable Object (one per mailbox id): handshake, queue, allow-list, delivery, the member's blob quota and live-room count, and the membership check for blob requests |
| `src/queue.ts` | SQLite queue and meta tables used by `Mailbox` |
| `src/room.ts` | `Room` Durable Object (one per nameplate): pairing relay, one joiner, 10 minute life |
| `src/blobmeta.ts` | `BlobMeta` Durable Object (one per blob): ACL, expiry, cleanup |
| `src/blobs.ts` | signed HTTP handlers for `/v1/blobs`, chunk bytes in R2 |
| `src/schema.ts` | creates a Durable Object's tables on its first write |
| `src/rpc.ts` | one retry for calls between Durable Objects, and error text for logs |

All Durable Objects are SQLite-backed. Mailboxes use the WebSocket Hibernation API, so an idle
connection costs nothing. A Durable Object creates its tables only on its first write (a
registration, a new room, a new blob), and a burned room or deleted blob removes all of its
storage, so unregistered keys, unknown nameplates and unknown blob ids leave nothing behind.

## Develop and test

You need Node.js 22 or newer.

```sh
cd relay-cf
npm ci
npm run typecheck
npm test            # Vitest inside workerd via @cloudflare/vitest-pool-workers; no account needed
cp .dev.vars.example .dev.vars
npm run dev         # wrangler dev on http://127.0.0.1:8787
```

`package.json` overrides `sharp` to `^0.35.4` because the test pool's bundled miniflare pins
`sharp` 0.35.2, which `npm audit` flagged; `sharp` is a development dependency only and is never
deployed.

## Deploy

You need Node.js 22 or newer (for `npx`) and a Cloudflare account; the free plan is enough.
Do these once in the Cloudflare dashboard first:

1. **Enable R2:** R2 Object Storage, then accept the plan (the free tier is
   enough). Until R2 is enabled, `wrangler r2 bucket create` fails.
2. **Create your workers.dev subdomain:** open Compute (Workers), then
   Workers & Pages. Opening it the first time creates the subdomain (you can
   rename it there). Until it exists, `wrangler deploy` fails with
   "You need a workers.dev subdomain" (code 10063). The relay's URL will be
   `https://cravv-relay.<subdomain>.workers.dev`. Renaming the subdomain later
   changes that URL, and a new name can take a few minutes before it answers
   over HTTPS; machines already set up must then move to the new URL
   (`cravv-connect setup --reset --relay <new url>`, and pair again).

Then, from a clone of this repository:

```sh
cd relay-cf
npm ci
npx wrangler login                        # opens a browser to authorize wrangler
npx wrangler r2 bucket create cravv-relay-blobs
npx wrangler r2 bucket lifecycle add cravv-relay-blobs expire-blobs blobs/ --expire-days 15

# The admin token: generate it into a private file and hand it to Cloudflare
# on stdin, so it never appears on screen or in your shell history.
(umask 077; openssl rand -hex 32 > ~/.cravv-relay-admin-token)
npx wrangler secret put ADMIN_TOKEN < ~/.cravv-relay-admin-token

npx wrangler deploy --var PUBLIC_ORIGIN:https://cravv-relay.<subdomain>.workers.dev
curl https://cravv-relay.<subdomain>.workers.dev/v1/health   # {"ok":true,"version":1}
```

The lifecycle rule is a backstop: `BlobMeta` deletes chunks on `DELETE` and at the 7 day TTL,
so R2 only keeps an object past 15 days if that cleanup failed. Check it with
`npx wrangler r2 bucket lifecycle list cravv-relay-blobs`.

`PUBLIC_ORIGIN` pins the relay origin that clients sign during auth (you can also put
`[vars] PUBLIC_ORIGIN = "..."` in `wrangler.toml`). Without it the relay falls back to the origin of
each request URL. Pass it again on every later `wrangler deploy`, or it is dropped. It must be
`scheme://host[:port]` over http or https with nothing after it; anything else (a path, a typo in
the scheme) makes the relay answer every request with `500 relay misconfigured: PUBLIC_ORIGIN` and log
the value once.

Set up the first machine on the relay (see the main README):

```sh
cravv-connect setup --relay https://cravv-relay.<subdomain>.workers.dev --relay-token - < ~/.cravv-relay-admin-token
```

Every other machine joins with `cravv-connect setup --join <code>`: its invite arrives inside the
encrypted pairing exchange, so the admin token is needed only once. Keep the token file private;
it lets anyone register new machines on your relay.

## Conformance against wrangler dev

From the repository root:

```sh
make conformance-cf
```

The target runs `relay-cf/scripts/conformance.sh`, which starts `wrangler dev` on
`http://127.0.0.1:8787` with a throwaway state directory, an admin token of
`conformance-admin-token` (handed over in an env file, never on a command line), and
`PUBLIC_ORIGIN=http://127.0.0.1:8787`, waits for `/v1/health`, then runs:

```sh
CRAVV_CONFORMANCE_ADMIN_TOKEN=conformance-admin-token go run ./cmd/cravv-conformance --relay http://127.0.0.1:8787
```

Extra arguments go to the suite, so `relay-cf/scripts/conformance.sh --slow` also fills a
mailbox to its 10000-frame and 50 MB caps. TTL cases are skipped automatically against an
external relay; the relay-cf Vitest suite covers them with `runInDurableObject` and `runDurableObjectAlarm`. Overrides: `PORT`, `ADMIN_TOKEN`,
`CONFORMANCE_CMD` (the command that receives `--relay`, with the token in
`CRAVV_CONFORMANCE_ADMIN_TOKEN`), and
`DISABLE_RATE_LIMITS=1` to relax both rate limits.

## Limits and overrides

Defaults match the cravv-connect spec: 256 KiB frames, 50 MB or 10000 frames per mailbox queue,
7 day queue TTL, 10 minute rooms and invites, 16 buffered room messages, 100 MiB blobs in
1 MiB chunks, 7 day blob TTL, 2 GiB and 256 live blobs per member (more is `413 too_large`),
50 GiB of live blobs across the relay (`413 too_large`), 20 unexpired invites per member and
8 live pairing rooms per member (more is `res{status:"error",code:"rate_limited"}`; a room frees
its slot when it burns or expires), 1000 requests per live mailbox
connection refilled at 200 per second, and 600 connects or blob requests per IP per minute. For local testing you can lower them in `.dev.vars` with
`QUEUE_MAX_FRAMES`, `QUEUE_MAX_BYTES`, `QUEUE_TTL_SECONDS`, `ROOM_TTL_SECONDS`,
`INVITE_TTL_SECONDS`, `BLOB_TTL_SECONDS`, `BLOB_QUOTA_BYTES`, `MAX_TOTAL_BLOB_BYTES`, `REQUEST_BURST`,
`REQUEST_RATE_PER_SECOND`, and `DISABLE_RATE_LIMITS=1`. Leave them unset in production.

## Protocol notes specific to this relay

- `GET /v1/connect` must carry `?ik=<base64 identity key>`. It only picks the mailbox Durable
  Object; the relay still requires the signed challenge from that same key, and a mismatch fails
  with `auth_failed`.
- The relay origin is `PUBLIC_ORIGIN` when set, otherwise `new URL(request.url).origin`, normalized
  to `scheme://host[:port]` with a lowercase host, no trailing dot, the default port dropped, and
  IPv6 bracketed. It must equal what the Go client computes from the relay URL it was given
  (`https://host` in production, `http://127.0.0.1:8787` locally). Both the WebSocket auth
  signature and the blob request signature
  (`cravv-http-v1\n<origin>\n<METHOD>\n<raw path>\n<ts>\n<hex sha256(body)>`) cover it.
- A request that fails inside the relay, such as a `send` whose call to the recipient's `Mailbox`
  throws, gets `res{status:"error",code:"internal"}` and the connection stays open, so acks and
  deliveries continue; the log records the op name and the error, never the request. Calls to
  other Durable Objects (`Mailbox.enqueue`, the `Registry`, `Room.init`) are retried once on a
  fresh stub when Cloudflare marks the error `retryable`, so a frame can be queued twice; clients
  deduplicate by `id`. A failure during the handshake still closes the connection with
  `error{internal}`.
