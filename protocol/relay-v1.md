# relay-v1: the cravv-connect relay protocol

Status: normative. Version 1.

A relay stores and forwards opaque, end-to-end encrypted frames between the mailboxes of
cravv-connect machines, hosts one-shot pairing rooms, and holds encrypted file chunks.
It never sees plaintext. Any server that implements this document and passes the
`conformance/` suite is a valid relay.

The key words MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY are used as in RFC 2119.

Go reference types for every message below live in `internal/relayproto`.

## 1. Conventions

- **Base64.** Every binary field (`ik`, `sig`, `nonce`, `frame`, `data`, and the
  `X-Cravv-IK` / `X-Cravv-Sig` headers) is standard base64 (RFC 4648 section 4, alphabet
  `A-Z a-z 0-9 + /`). Senders MUST emit it without `=` padding. Receivers MUST accept it
  with or without padding.
- **Identity key (IK).** A 32-byte Ed25519 public key.
- **Mailbox ID.** `lowercase(base32(SHA-256(ik)))` with the RFC 4648 alphabet and no
  padding: always 52 characters. It equals the machine ID used by peers.
- **Origin.** `scheme://host[:port]` of the relay as the client dials it, in this
  normalized form (both sides MUST normalize before signing or verifying):
  - `scheme` is `http` or `https`, lowercase;
  - `host` is lowercase, without a trailing dot; an IPv6 literal is written in brackets
    (`[::1]`);
  - the port is omitted when it is the scheme's default (`443` for `https`, `80` for
    `http`) and written in decimal otherwise;
  - no user info, path, trailing slash, query, or fragment.

  For example `HTTPS://Relay.Example.com.:443/` normalizes to `https://relay.example.com`,
  and `http://127.0.0.1:8787` is already normal. The Go reference is
  `relayproto.NormalizeOrigin`. A relay SHOULD be configured with its public origin. It MUST
  NOT derive the origin from client-controlled request headers (such as `Host` behind a
  proxy), because the origin is what stops a signature made for one relay from being
  replayed to another. A relay MAY use the request URL's origin when the platform itself
  routes by hostname (for example Cloudflare Workers), so the client cannot choose it.
- **Time.** Relay timestamps are UNIX seconds.
- **JSON.** Field names are `snake_case`. Receivers MUST ignore unknown fields.

## 2. Endpoints

| Method and path | Purpose |
|---|---|
| `GET /v1/health` | Liveness: `200 {"ok":true,"version":1}` |
| `GET /v1/connect?ik=<b64 ik>` (WebSocket) | Mailbox connection (section 3) |
| `GET /v1/pair/{nameplate}[?token=<creator_token>]` (WebSocket) | Pairing room (section 5) |
| `POST /v1/blobs` | Create a blob (section 6) |
| `PUT /v1/blobs/{id}/chunks/{n}` | Upload chunk `n` |
| `GET /v1/blobs/{id}/chunks/{n}` | Download chunk `n` |
| `DELETE /v1/blobs/{id}` | Delete a blob |

In production the relay MUST be served over TLS (`https`, `wss`). Plain `http` / `ws` is
for local development and tests only.

## 3. Mailbox connection: `GET /v1/connect`

### 3.1 Framing

- One JSON object per WebSocket **text** message. Every message has a string field `t`.
- A client request that expects an answer carries `rid`, a client-chosen string. The
  server answers it with exactly one `res` carrying the same `rid`. `ack` has no `rid` and
  no answer.
- The server MUST process the messages of one connection in the order received, so a
  request sent after an `ack` observes that `ack`.
- Replies and pushes share the socket: a `deliver` MAY arrive before the `res` of a
  request sent earlier (for example a machine sending to itself). Clients MUST match
  replies by `rid` and MUST NOT assume any ordering between `res` and `deliver`.
- The server MUST accept a message of at least `4/3 * 262144 + 16384` bytes (a `send`
  carrying a maximum frame). Clients MUST accept messages of at least 1 MiB (a `deliver`
  carrying a maximum frame is about 350 KB).

### 3.2 Errors and closing

A fatal problem is reported with an `error` message, after which the server closes the
WebSocket with status 1008 (policy violation):

```json
{"t":"error","code":"auth_failed","message":"bad signature"}
```

`message` is human-readable and not stable. Clients act on `code` only.

### 3.3 Routing hint

The upgrade URL MUST carry `ik`, the base64 of the client's IK (URL-encoded as a query
value). It lets a relay route the connection (for example to a per-mailbox Durable Object)
before the handshake. It proves nothing:

- If `ik` is missing or is not a valid 32-byte key, the server MUST accept the upgrade,
  send `error{code:"bad_request"}`, and close.
- The server MUST still run the full handshake, and MUST reject with
  `error{code:"auth_failed"}` when the `ik` in `auth` differs from the query `ik`.

### 3.4 Handshake

```
client                                   server
  | hello {versions:[1]}                    |
  |---------------------------------------->|
  |                      welcome {version:1}|
  |                        challenge {nonce}|
  |<----------------------------------------|
  | auth {ik, sig}                          |
  |---------------------------------------->|
  |        auth_ok {registered, mailbox_id} |
  |<----------------------------------------|
  | (only if registered=false)              |
  | register {rid, admin_token | invite}    |
  |---------------------------------------->|
  |                    res {rid, status:ok} |
  |<----------------------------------------|
```

1. The client sends `hello`:
   ```json
   {"t":"hello","versions":[1]}
   ```
   The first message MUST be `hello`; anything else gets `error{bad_request}`. If
   `versions` does not contain `1`, the server sends `error{unsupported_version}`.
2. The server sends `welcome` and then `challenge`:
   ```json
   {"t":"welcome","version":1}
   {"t":"challenge","nonce":"q4Sx3m9mV2d5c0c5a0K1u4d8b2y9oYl1h3n0z7Qd1wE"}
   ```
   The nonce MUST be at least 16 (reference relays use 32) fresh random bytes, new for
   every connection.
3. The client answers with `auth`:
   ```json
   {"t":"auth","ik":"Vt7lW0...","sig":"3x9Qa..."}
   ```
   `sig` is Ed25519 by the IK over the UTF-8 bytes of

   ```
   cravv-relay-auth-v1\n<origin>\n<nonce>
   ```

   where `<nonce>` is the challenge string exactly as received. The server MUST send
   `error{auth_failed}` if `ik` is not a valid key, differs from the query `ik`, or the
   signature does not verify against the relay's configured origin.
4. The server replies:
   ```json
   {"t":"auth_ok","registered":false,"mailbox_id":"t7o6kvr6wm75caaod3ncxgkyibsskt3r36pptavd6ph3i2x3mfja"}
   ```
5. If `registered` is false, the next client message MUST be `register`; any other
   message gets `error{not_registered}`.
   ```json
   {"t":"register","rid":"register","admin_token":"..."}
   {"t":"register","rid":"register","invite":"k3v..."}
   ```
   The server accepts it if `admin_token` equals the relay's admin token (compared in
   constant time; an empty token never matches) or `invite` is a known, unused,
   unexpired invite, which it consumes atomically. Success:
   ```json
   {"t":"res","rid":"register","status":"ok"}
   ```
   Failure is `{"t":"res","rid":"register","status":"error","code":"forbidden"}`, after
   which the server MUST close the connection. Clients treat it as final.
6. The connection is now live (section 3.5). A `register` sent on a live connection by an
   already registered mailbox gets `res{status:"ok"}` and changes nothing.

A server SHOULD bound the whole handshake with a timeout (the reference relay uses 10
seconds).

### 3.5 Live connection

**Single live connection.** A mailbox has at most one live connection. When a new
connection for the same mailbox completes the handshake, the server MUST send
`error{code:"gone"}` to the older one and close it.

**Delivery.** As soon as the connection is live, the server MUST push every unacked,
unexpired frame in the mailbox queue as `deliver`, in ascending `seq`, and then push new
frames as they are queued:

```json
{"t":"deliver","seq":42,"from":"<b64 sender ik>","id":"01JAB...","frame":"<b64 bytes>"}
```

- `seq` is per mailbox, starts at 1, increases by exactly one for every accepted frame,
  is persistent, and MUST NOT be reused, even after the queue empties or the relay
  restarts.
- `from` is the IK the sender authenticated with. The relay MUST NOT let a sender choose it.
- `id` and `frame` are exactly what the sender sent.
- Within one connection the server MUST NOT push the same `seq` twice. A frame that was
  pushed but not acked MUST be pushed again on the next connection. Delivery is therefore
  at least once, and clients deduplicate by `id`.

**Keepalive.** Clients SHOULD send a WebSocket ping every 30 seconds on a live
connection and SHOULD treat a ping that gets no pong (the reference client waits 15
seconds) as a dead connection: close it and reconnect. Relays MUST answer pings with
pongs (RFC 6455) and SHOULD NOT close an authenticated connection for being idle
before 120 seconds without any message or ping from the client.

### 3.6 Client requests

Each request below is answered by one `res`:

```json
{"t":"res","rid":"7","status":"ok","code":"","invite":"","nameplate":"","creator_token":""}
```

Empty fields are omitted on the wire. `status` is one of the values in section 3.7; when
it is `error`, `code` says why (section 3.8).

**allow / deny.** Add or remove a sender on this mailbox's allow-list. Only senders on the
list can queue frames here. The list is keyed by the sender's mailbox ID.
```json
{"t":"allow","rid":"1","ik":"<b64 ik>"}
{"t":"deny","rid":"2","ik":"<b64 ik>"}
```
Reply `ok`, or `error` with `bad_request` if `ik` is not a valid key. Both are idempotent.
`deny` only stops future `send`s: frames the sender queued before the `deny` stay in
the queue and are still delivered.

**invite_request.** Mint a single-use registration invite valid for 10 minutes.
```json
{"t":"invite_request","rid":"3"}
{"t":"res","rid":"3","status":"ok","invite":"k3vq7a2m5n6c4d9e0f1g2h3i4j"}
```
Invites are opaque strings of at most 64 characters, unique, and unguessable (at least
128 random bits).

**room_create.** Create a pairing room (section 5) that lives 10 minutes.
```json
{"t":"room_create","rid":"4"}
{"t":"res","rid":"4","status":"ok","nameplate":"7K3F","creator_token":"9f2c4e6a8b0d1f3e5a7c9b1d3f5e7a9c"}
```
`nameplate` is 4 characters from the Crockford base32 alphabet
`0123456789ABCDEFGHJKMNPQRSTVWXYZ`, unique among live rooms. `creator_token` is an opaque
secret of at least 128 random bits (reference relays use 32 lowercase hex characters).

**send.** Queue a frame for another mailbox.
```json
{"t":"send","rid":"5","to":"<recipient mailbox id>","id":"01JAB...","frame":"<b64 bytes>"}
```
`id` is 1 to 128 characters. The server MUST check, in this order:

| Check | Reply status |
|---|---|
| sender over its rate limit (applies to every request type except `ack`) | `rate_limited` |
| malformed message, empty `to` or `id`, `id` over 128 chars, `frame` not base64 | `error` + `bad_request` |
| decoded `frame` longer than 262144 bytes | `too_large` |
| `to` is not a registered mailbox | `unknown_mailbox` |
| sender's mailbox not on the recipient's allow-list | `not_allowed` |
| recipient queue would exceed 10000 frames or 52428800 bytes of decoded frames (expired frames excluded) | `queue_full` |
| otherwise the frame is appended with the next `seq` | `queued` |

A client keeps a frame in its own outbox until it sees `queued`, and resends with the
same `id` after a reconnect.

The `unknown_mailbox` check deliberately comes before `not_allowed`: any member can
learn whether a mailbox ID is registered on the relay. This is accepted because only
members can send, mailbox IDs are hashes of public keys, and a precise status lets a
sender tell "peer not set up yet" from "peer paused me".

**ack.** Acknowledge delivery. No `rid`, no reply.
```json
{"t":"ack","seq":42}
```
The server MUST remove every queued frame of this mailbox with `seq <= 42`. Acking a
`seq` that was never delivered is allowed and removes only frames that exist. A malformed
`ack` gets `error{bad_request}` and the connection closes.

**Unknown or malformed requests.** An unknown `t` that carries a `rid` gets
`res{status:"error", code:"bad_request"}` and the connection stays open. An unknown `t`
without `rid`, a known request type (other than `ack`) without `rid`, a binary message,
or invalid JSON gets `error{bad_request}` and the connection closes.

### 3.7 Status values (`res.status`)

| Status | Meaning |
|---|---|
| `ok` | Request done |
| `queued` | Frame accepted and assigned a `seq` |
| `not_allowed` | Recipient has not allowed the sender (for a daemon: paused by the peer) |
| `queue_full` | Recipient queue is at a cap; retry later |
| `too_large` | Decoded frame over 262144 bytes; use blobs instead |
| `unknown_mailbox` | No registered mailbox with that ID |
| `rate_limited` | Sender over its per-mailbox budget; retry later |
| `error` | See `code` |

### 3.8 Error codes (`error.code`, `res.code`, HTTP body `code`)

| Code | Meaning |
|---|---|
| `bad_request` | Malformed message, missing field, or message not allowed at this point |
| `unsupported_version` | No common protocol version |
| `auth_failed` | Bad key, bad signature, or `ik` differs from the routing `ik` |
| `not_registered` | Unregistered key sent something other than `register` |
| `forbidden` | Registration refused, wrong creator token, or caller not allowed |
| `not_found` | Unknown room, blob, or chunk |
| `gone` | Replaced by a newer connection, room already used or expired, blob expired |
| `rate_limited` | Too many requests or connections |
| `too_large` | Over a size limit |
| `internal` | Relay failure; retry later |

## 4. Queue lifetime

- A queued frame expires 7 days after it was accepted. Expired frames MUST NOT be
  delivered and MUST NOT count toward the caps. A relay MAY drop them lazily, at read time.
- The caps are per recipient mailbox: 10000 frames and 52428800 bytes of decoded frames.

## 5. Pairing rooms: `GET /v1/pair/{nameplate}`

A room carries opaque PAKE messages between exactly two parties: the **creator**, who made
it with `room_create` and connects with `?token=<creator_token>`, and one **joiner**, who
connects without a token. The joiner is not authenticated; knowledge of the nameplate is
enough to join, and the PAKE secret protects the exchange.

- Nameplates are case-insensitive: the server MUST treat `7k3f` as `7K3F`.
- Unknown nameplate, or one whose room was already deleted: `error{not_found}`.
- Room past its 10-minute lifetime: `error{gone}` (a relay MAY answer `error{not_found}` once it
  has purged the room).
- Wrong creator token: `error{forbidden}`.
- A second creator connection: `error{gone}`.
- A joiner that arrives before the creator has connected: `error{not_found}` (the join is
  not consumed).
- A second joiner: `error{gone}`. Only one join is ever allowed per room.

Frames, all JSON text messages:

```json
{"t":"waiting"}
{"t":"peer_joined"}
{"t":"msg","data":"<b64 bytes>"}
{"t":"closed"}
```

1. When the creator connects, the server sends `waiting` exactly once.
2. When the joiner connects, the server sends `peer_joined` to the joiner and then to the
   creator.
3. Either side sends `msg`; the server forwards it unchanged to the other side. `data`
   decodes to at most 262144 bytes (`error{too_large}` otherwise). Messages the creator
   sends before the joiner arrives MUST be buffered (up to 16) and delivered to the
   joiner, in order, right after its `peer_joined`.
4. Anything other than `msg` from a client gets `error{bad_request}` and ends the room
   (the other side gets `closed`).
5. When either side disconnects, the server sends `closed` to the other side, closes it
   with status 1000, and deletes the room. The nameplate is burned: later connections get
   `not_found`.
6. When the lifetime ends while sides are connected, the server sends `error{gone}` to
   every connected side, closes them, and deletes the room.

## 6. Blobs over HTTP

Files travel as encrypted chunks. The relay stores them opaquely and enforces access.

### 6.1 Request signing

Every blob request MUST carry three headers:

| Header | Value |
|---|---|
| `X-Cravv-IK` | base64 of the caller's IK |
| `X-Cravv-TS` | UNIX seconds, decimal |
| `X-Cravv-Sig` | base64 Ed25519 signature by the IK over the string below |

```
cravv-http-v1\n<origin>\n<METHOD>\n<path>\n<ts>\n<hex(sha256(body))>
```

- `<origin>` is the relay's normalized origin (section 1). The client signs the origin it
  dialed; the server verifies against its own configured origin, never one taken from
  request headers, so a signed request cannot be replayed to another relay.
- `<METHOD>` is the request method as sent (`GET`, `PUT`, `POST`, `DELETE`).
- `<path>` is the **raw** request path exactly as it appears on the request line, still
  percent-encoded, without the query string (for example `/v1/blobs/abc/chunks/0`). The
  client signs the path it sends; the server verifies over the path it received, without
  decoding it. Blob IDs use only `[a-z0-9]`, so in practice the raw and decoded paths are
  identical.
- `<ts>` is the `X-Cravv-TS` header value.
- The hash is lowercase hex; an empty body hashes to
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.

Example: a `PUT` of the 2-byte body `hi` to `https://relay.example.com` at `ts`
1700000000 signs

```
cravv-http-v1
https://relay.example.com
PUT
/v1/blobs/abc/chunks/0
1700000000
8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4
```

(lines joined by `\n`, no trailing newline).

The server MUST reject with `401` when a header is missing, the key or signature is
malformed, the signature does not verify (including a signature made for another
origin), or `ts` is more than 300 seconds away from the server clock. The signer MUST be
a registered mailbox; otherwise `403`.

### 6.2 Operations

**Create.** `POST /v1/blobs`, body at most 4 KiB:
```json
{"size":1048580,"chunks":2,"recipient":"<b64 recipient ik>"}
```
`size` is the plaintext size in bytes. Reply `201`:
```json
{"blob_id":"q2w3e4r5t6y7u8i9o0p1a2s3d4"}
```
- `size > 104857600`: `413`.
- `chunks` must be between 1 and `max(1, ceil(size / 1048576))`; otherwise `400`.
- `recipient` not a valid key: `400`.
- The total `size` of the caller's unexpired blobs would exceed the relay's per-member
  quota (at least 1 GiB; reference relays use 2 GiB): `413`.
- Blob IDs are opaque, unguessable, at most 64 characters (reference relays use 26
  lowercase base32 characters). The blob expires 7 days after creation.

**Upload chunk.** `PUT /v1/blobs/{id}/chunks/{n}` with the raw chunk bytes. Only the
uploader may call it. The body is at most 1048640 bytes (1 MiB plus 64 bytes of AEAD
overhead); larger is `413`. `n >= chunks` is `400`. Re-uploading a chunk replaces it. The
sum of stored chunk sizes may not exceed `size + 64 * chunks` (`413`). Reply `204`.

**Download chunk.** `GET /v1/blobs/{id}/chunks/{n}`. Only the recipient may call it. Reply
`200` with the raw bytes (`Content-Type: application/octet-stream`), `404` if that chunk
was not uploaded, `400` if `n >= chunks`.

**Delete.** `DELETE /v1/blobs/{id}`. The uploader or the recipient. Reply `204`. The
recipient deletes after a complete download.

### 6.3 Status codes and error body

Checks happen in this order: rate limit (`429`), body size (`413`), signature (`401`),
membership (`403`), blob exists (`404`), blob expired (`410`), caller's role (`403`),
chunk index (`400`), then the operation.

| Status | Meaning |
|---|---|
| `400` | Malformed request, bad chunk count or index |
| `401` | Missing or bad signature, or clock skew over 300 seconds |
| `403` | Not a member, or the wrong party for this operation |
| `404` | Unknown blob or chunk; also any blob after `DELETE` |
| `410` | Blob expired (a relay MAY answer `404` once it has purged it) |
| `413` | Over the blob size, chunk size, declared size, or storage quota |
| `429` | Rate limited |

Every non-2xx response has a JSON body:
```json
{"code":"forbidden","message":"only the recipient may read"}
```
with `code` from section 3.8.

## 7. Rate limits

- A relay SHOULD limit requests per mailbox on a live connection. Over the limit, a
  request gets `res{status:"rate_limited"}` and the connection stays open. The reference
  relay uses a token bucket of 1000 requests refilled at 200 per second.
- A relay SHOULD limit new WebSocket connections and blob requests per client IP. Over the
  limit, a WebSocket gets `error{rate_limited}` and is closed; an HTTP request gets `429`.
  The reference relay uses a bucket of 500 refilled at 50 per second.
- Operators MAY relax or disable both limits. Clients MUST treat `rate_limited` and `429`
  as "retry later with backoff".

## 8. Limits summary

| Item | Value |
|---|---|
| Frame (`send`, room `msg`) | 262144 bytes decoded |
| Mailbox queue | 10000 frames or 52428800 bytes |
| Queue TTL | 7 days |
| Invite lifetime | 10 minutes, single use |
| Room lifetime | 10 minutes, one joiner |
| Room buffer before join | 16 messages |
| Blob | 104857600 bytes plaintext, TTL 7 days |
| Chunk upload body | 1048640 bytes |
| Blob create body | 4096 bytes |
| HTTP signature skew | 300 seconds |
| Handshake timeout | SHOULD, 10 seconds in the reference relay; optional for hibernating relays |

## 9. What a conforming relay MUST NOT do

- Accept `auth` without verifying the signature against its own configured origin.
- Let an unregistered key do anything but `register`.
- Accept an invite twice, or after its lifetime.
- Queue a frame from a sender that is not on the recipient's allow-list.
- Reuse a `seq`, reorder a mailbox's frames, or drop an unacked, unexpired frame.
- Keep two live connections for one mailbox.
- Let a second joiner into a room, or reuse a nameplate's room after it was burned.
- Serve a chunk to anyone but the recipient, or accept one from anyone but the uploader.
- Inspect, alter, or log frame, message, or chunk contents.

## 10. Conformance

`conformance/` is a black-box Go suite that checks every MUST above over the wire.

- In-process, with a fake clock (includes the TTL cases): `go test ./conformance`.
- Against a running relay (TTL cases are skipped because the relay's clock cannot be
  advanced):
  ```
  go run ./cmd/cravv-conformance --relay http://127.0.0.1:8787 --admin-token <token>
  ```
  Add `--slow` (or set `CRAVV_CONFORMANCE_SLOW=1`) to also fill a mailbox to 10000 frames
  and to 50 MB.
