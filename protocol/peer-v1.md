# peer-v1: end-to-end messages between cravv-connect machines

Status: normative. Version 1.

peer-v1 is what two paired machines say to each other. Every message travels
through a relay (see `relay-v1.md`) as an opaque frame: the relay sees the
sender's identity key, the recipient mailbox, a message ID, the frame size and
the time. For files it also sees the recipient, the size and the chunk
count, and for pairing the nameplate. It never sees message contents. This document covers:

1. identities and prekeys,
2. the sealed frame format,
3. how a receiver opens, checks and confirms frames,
4. every message kind and its body,
5. prekey rotation and the stale prekey round trip,
6. file transfer,
7. the pairing protocol that introduces two machines.

Go reference: `internal/core` (envelope and bodies), `internal/keys`,
`internal/sealing`, `internal/filecrypt`, `internal/pake`, `internal/bindcode`,
`internal/pathguard`, and `internal/daemon` (outbound, inbound, handlers,
policygate, pairing, files, tasks, prekeys, peers, killswitch).

The key words MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.

## 1. Conventions

- **JSON.** Field names are `snake_case`. Byte fields are standard base64 with
  padding (RFC 4648 section 4), which is how Go encodes `[]byte`. Receivers
  ignore unknown fields.
- **IDs.** Message, task and file IDs are 26 characters of upper-case Crockford
  base32 (`0-9 A-Z` without `I L O U`): 48 bits of UNIX milliseconds followed
  by 80 random bits, so they sort by creation time. Receivers reject any
  message whose envelope ID, `task_id` or `file_id` (including those in
  `files` lists) is not exactly that, and any `blob_id` that is not 1 to 64
  characters of `[a-z0-9]` (relay-v1 section 6.2). A rejected message is
  dropped and acknowledged, never retried, and gets no `control.delivered`
  when its envelope ID is the bad one. IDs are shown in terminals and agent
  prompts, so nothing else may get through.
- **Timestamps** in envelopes and prekeys are UNIX milliseconds.
- **Machine ID.** `lowercase(base32(SHA-256(ik)))`, RFC 4648 alphabet, no
  padding: 52 characters. It is also the relay mailbox ID. Displays shorten it
  to the first 16 characters.

## 2. Keys

| Key | Algorithm | Lifetime | Use |
|---|---|---|---|
| Identity key (IK) | Ed25519 | Until `cravv-connect reset-identity` | Relay authentication, signs every frame and every prekey |
| Prekey (PK) | X25519 | Current for 7 days; the private key is kept 21 days after it is superseded | Receives sealed frames |

A **signed prekey** is:

```json
{
  "id": "01J8ZQ3W5TAV9M2C4XKQ7N6B1D",
  "pub": "6J3m0p4xJ3g0m3kq3Q1ZyW2VYk0c1C1QfI2yq3VYl2M=",
  "created_at": 1790000000000,
  "sig": "base64 of a 64-byte Ed25519 signature"
}
```

`sig` is the IK signature over the UTF-8 string

```
"cravv-prekey-v1\n" + id + "\n" + base64(pub) + "\n" + decimal(created_at)
```

where `base64` is standard base64 with padding. A receiver MUST verify a prekey
against the peer's IK before storing or using it.

## 3. The sealed frame

### 3.1 Envelope (the plaintext)

```json
{
  "v": 1,
  "id": "01J8ZQ9K7B2V4N6M8P0R2T4W6Y",
  "ts": 1790000123456,
  "from_machine": "q7w...52 chars",
  "from_session": "claude@glow-v2",
  "to_machine": "k3d...52 chars",
  "to_session": "codex@training",
  "kind": "chat",
  "body": {"text": "the build is green"}
}
```

| Field | Meaning |
|---|---|
| `v` | Always `1` |
| `id` | Message ID, unique per sender; the deduplication key |
| `ts` | Sender clock, UNIX milliseconds |
| `from_machine`, `to_machine` | Machine IDs |
| `from_session` | Sending agent session, when there is one (omitted for daemon-generated messages) |
| `to_session` | Target session on the receiver; omitted for machine-wide delivery |
| `kind` | See section 5 |
| `body` | JSON object whose shape depends on `kind` |

### 3.2 Header

```json
{"v":1,"id":"01J8ZQ9K7B2V4N6M8P0R2T4W6Y","from_machine":"q7w...","to_machine":"k3d...","pk_id":"01J8ZQ3W5TAV9M2C4XKQ7N6B1D","suite":"hpke-x25519-sha256-chacha20poly1305"}
```

The **canonical header** is exactly this JSON: the fields in the order `v`,
`id`, `from_machine`, `to_machine`, `pk_id`, `suite`, with no whitespace. Both
the HPKE info and the signature cover these bytes, so a sender MUST produce
them byte for byte and a receiver MUST recompute them from the parsed fields
rather than reuse the received text.

- `id`, `from_machine`, `to_machine` repeat the envelope's values.
- `pk_id` is the ID of the recipient's prekey the frame is sealed to.
- `suite` names the sealing suite. v1 defines one suite (3.3). New suites are
  added by registration; a receiver rejects unknown suites.

### 3.3 Suite `hpke-x25519-sha256-chacha20poly1305`

HPKE (RFC 9180) Base mode with DHKEM(X25519, HKDF-SHA256), HKDF-SHA256 and
ChaCha20-Poly1305, used single-shot:

- **Recipient key:** the X25519 prekey named by `pk_id`.
- **info:** `"cravv-connect/peer-v1\n" + canonical header`. The single-shot API
  has no separate AAD, so the header is bound to the ciphertext through `info`:
  changing any header field makes decryption fail.
- **Plaintext:** the envelope JSON.
- **Payload:** `enc || ciphertext` (32-byte encapsulated key, then the AEAD
  output with its 16-byte tag).

### 3.4 Frame

```json
{"h": {canonical header fields}, "p": "base64(payload)", "s": "base64(signature)"}
```

`s` is the sender's IK signature over

```
"cravv-frame-v1\n" + canonical header + "\n" + payload
```

The marshalled frame MUST be at most 262144 bytes (the relay frame limit).
Anything bigger travels as a file (section 6).

The frame goes to the relay as `send{to: to_machine, id: header.id, frame}`.
The relay-level `id` equals the header ID, so a resend of the same message
reuses it.

## 4. Receiving

A receiver processes deliveries from its mailbox in `seq` order. While the
kill switch is on it handles and acks nothing: the delivery in hand stays at
the relay and inbound processing stops until the switch is turned off. For
each delivery:

1. **Sender.** Look up the paired peer whose IK the relay reports as the
   sender. Unknown senders are dropped.
2. **Paused.** If this machine paused the peer, drop the frame.
3. **Parse** the frame: at most 262144 bytes, `h.v` is 1, `h.id` and `h.pk_id`
   are 1 to 64 ASCII letters or digits, `h.from_machine` and `h.to_machine`
   are 52 characters of `[a-z2-7]`, `h.suite` is 1 to 64 printable ASCII
   characters, and `p` and `s` are non-empty. Otherwise drop.
4. **Open**, in this order:
   1. verify `s` against the peer's IK;
   2. `h.from_machine` equals the signer's machine ID;
   3. `h.to_machine` equals this machine;
   4. `h.suite` is known;
   5. the private prekey `h.pk_id` exists (current or superseded and not yet
      purged). If it does not, reply with `control.stale_prekey` (section 5.3),
      ack the delivery, and stop. The reply is sent at most once per sender and
      message ID (recorded in the deduplication store), so a relay redelivery
      of the same frame does not trigger another resend;
   6. decrypt;
   7. the envelope has `v` 1 and its `id`, `from_machine`, `to_machine` equal the
      header's. This stops a valid signature from being stripped and replaced.
5. **Freshness.** Drop the message if `ts` is more than 21 days in the past or
   more than 10 minutes in the future. Future timestamps are counted, and
   `status` warns about clock skew.
6. **Replay.** If `id` is already in the deduplication store (kept 30 days),
   do not handle it again; for non-control kinds, confirm it again with
   `control.delivered` (the sender may have missed the first receipt).
7. **Handle** it through the handler registered for `kind`. Kinds with no
   handler are dropped. `task.create` and `file.offer` pass through a policy
   gate first (section 5.2 and section 6): it applies the trust level, and an
   item the level rejects never reaches the normal handler.
8. **Record** `id` in the deduplication store. This happens only after the
   handler succeeded or failed for good, so a retryable failure leaves the
   message unrecorded and a redelivery runs the handler again. Handlers are
   therefore idempotent on their own: a crash between the handler's write and
   this step must not store the item twice. Tasks and files check their own
   records; a `chat` whose `id` is already in the inbox is not stored again
   (the receiver also keeps a unique index on the chat's `id` and target
   session).
9. **Confirm.** For every non-control kind, queue the ID for a
   `control.delivered` receipt to the sender.
10. **Ack** the relay `seq`.

Dropped frames are logged and counted in `status`. A handler failure that is
worth retrying (a local database error, including a failed deduplication
lookup) is not acked: the receiver stops reading, closes the connection and
reconnects after a backoff (1 second, doubling, at most 5 minutes), and
because acks are cumulative the relay redelivers from that frame. Every other handler failure is logged,
recorded, confirmed and acked, so the sender stops resending an item that
can never be used.

Receipts are batched per peer. The batch is sent when a burst of deliveries
ends and at least every 200 ms.

### 4.1 Sending and the outbox

Every outgoing message except `control.paused` and `control.unpaired` goes
through the persistent outbox. The item stores the envelope, not the frame, and
is sealed again on every attempt to the peer's current prekey. A message whose
sealed frame could not fit the 262144-byte limit is refused before it is
queued (`too_large`).

While the kill switch is on, the daemon still writes items to the outbox (for
example `task.update` notices), but sends nothing until the switch is turned
off; the queued items then go out. Agents cannot add messages while killed,
because the local API refuses sends (ipc-v1).

| Outbox state | Meaning |
|---|---|
| `pending` | Waiting for its next attempt |
| `queued` | The relay answered `queued`; waiting for `control.delivered` (non-control kinds only) |
| `held` | Not sent because a side paused the other (control kinds are never held) |

The relay's answer to `send` decides what happens:

| Relay status | Sender action |
|---|---|
| `queued` | Non-control kinds: mark `queued`; if no `control.delivered` arrives within 7 days (the relay queue TTL), the item goes back to `pending` and is sent again. Control kinds: delete the item (they are never confirmed) |
| `not_allowed` | Within 30 minutes of pairing: the peer has most likely not finalized yet (it has not allowed this machine on the relay); retry with backoff capped at 5 seconds and mark nothing. Later: the peer paused this machine: mark the peer "paused by peer" and hold every non-control item for it until `control.resumed`. Control items keep retrying with backoff |
| `too_large`, `unknown_mailbox` | Drop the item and report it in `status` (items for a peer that is no longer paired are dropped the same way) |
| `queue_full`, `rate_limited`, network error | Back off: 1 second, doubling per attempt, at most 5 minutes |

`control.delivered` deletes the confirmed items (only items addressed to the
peer that sent the receipt). Items older than 21 days are purged. Receivers
never confirm control kinds, so a control item leaves the outbox as soon as
the relay has queued it; the relay's at-least-once delivery covers it from
there.

`control.paused` and `control.unpaired` are sent once, directly, when the
relay connection is up (best effort), because the peer record changes right
after.

## 5. Kinds

Size limits: chat text, task instructions, task results and notes are at most
65536 bytes each.

### 5.1 Chat

`chat`: `{"text": "..."}`

```json
{"text": "tests pass on the GPU box, pushing now"}
```

Delivered at every trust level. With `to_session`, only that session sees it.
If that session does not exist when the message arrives, or it ends and is
not reclaimed within its grace period (5 minutes; 30 days for the `--json`
CLI), the item becomes machine-wide with the note
`(originally for <session>)`.

### 5.2 Tasks

`task.create`:

```json
{
  "task_id": "01J8ZR0A1B2C3D4E5F6G7H8J9K",
  "instructions": "Run the eval suite on checkpoint 12 and report the F1.",
  "files": [{"file_id": "01J8ZR0...", "name": "eval.yaml", "size": 812}]
}
```

`files` lists attachments sent as `file.offer` messages with the same
`task_id`. The receiver applies its trust level for the sender:

| Receiver's trust in sender | Result |
|---|---|
| chat-only | `rejected`, update note `not permitted` |
| ask-first | `awaiting_approval` until a human approves (then `queued`) or denies (`rejected`, note `denied by the receiving human`); expires after 24 hours (`expired`) |
| autonomous | `queued`; unclaimed after 24 hours it becomes `expired` |

A `task.create` whose `task_id` is already known is ignored (a redelivery of
the message that created a queued task only completes a delivery that an
earlier attempt left unfinished).

The receiver reports held and refused tasks with a `task.update`
(`awaiting_approval`, then `queued` after approval, or `rejected`); a task
queued at once is reported when it is claimed. Approval is a
human action on the receiver (`cravv-connect approvals`; the daemon raises a
desktop notification when a task starts waiting). Approving checks the peer
again: it is refused when the peer is no longer paired, has been paused by
this machine, or its trust level no longer allows tasks. Pausing or unpairing
a peer rejects its tasks awaiting approval (note `peer paused` or
`peer unpaired`).

`task.update` (receiver to sender):

```json
{"task_id": "01J8ZR0A1B2C3D4E5F6G7H8J9K", "state": "done", "result": "F1 = 0.913", "files": []}
```

| Field | Meaning |
|---|---|
| `state` | `awaiting_approval`, `queued`, `claimed`, `running`, `done`, `failed`, `cancelled`, `rejected`, or `expired` |
| `note` | Progress note or reason (`not permitted`, `killed`, `abandoned`, ...) |
| `result` | Final result text (with `done`) |
| `files` | Result files, sent as `file.offer` with the same `task_id` |

The envelope's `from_session` is the claiming session and `to_session` is the
session that created the task. The sender only accepts updates for its own
outbound tasks from the peer the task was sent to, and never moves a task
backwards: the order is `sent` < `awaiting_approval` < `queued` < `claimed` <
`running` < every terminal state. An update ranked lower than the current
state still adds its note, result and files. Updates after a terminal state
are ignored, and an update with state `sent` or an unknown state is rejected.

Receiver-side transitions:

```
awaiting_approval -> queued | rejected | expired | cancelled
queued            -> claimed | expired | cancelled | rejected
claimed           -> running | done | failed | cancelled
running           -> done | failed | cancelled
```

A claim is atomic: exactly one session wins. If the claiming session ends and
is not reclaimed within 5 minutes, the task becomes `failed` with note
`abandoned`. Sessions of the `--json` CLI (agent `cli`) are reclaimable for 30
days, so a task they claimed fails as `abandoned` once it has been claimed or
running for 7 days. The kill switch fails claimed and running tasks with note
`killed`. Lowering a peer to chat-only rejects its pending tasks (awaiting
approval or queued) with note `not permitted`.

`task.cancel` (sender to receiver): `{"task_id": "..."}`. Only the session
that created the task can cancel it, and the sender marks its copy
`cancelled` before sending. The receiver accepts it only from the peer that
sent the task: an `awaiting_approval`, `queued`, `claimed` or `running` task
becomes `cancelled` with note `cancelled by sender`, and the claiming session
(or every session, if unclaimed) gets a `task.update` inbox item with that
note. No `task.update` is sent back; unknown or finished tasks are ignored.

### 5.3 Control

| Kind | Body | Sent when |
|---|---|---|
| `control.prekey` | `{"prekey": signed prekey}` | After a rotation, to every peer this machine has not paused |
| `control.stale_prekey` | `{"msg_id": "...", "prekey": signed prekey}` | A frame arrived sealed to an unknown or purged prekey (once per sender and message ID) |
| `control.delivered` | `{"ids": ["...", "..."]}` | Non-control messages were stored (4, step 8) |
| `control.paused` | `{}` | This machine paused the peer (sent directly, then the peer is denied on the relay) |
| `control.resumed` | `{}` | This machine resumed the peer, or finalized pairing with it |
| `control.unpaired` | `{}` | This machine unpaired the peer (sent directly, best effort) |
| `control.relay_moved` | `{"relay_url": "https://..."}` | Reserved for relay changes. v1 daemons accept and store it (only `https` URLs, or `http` for localhost and private network addresses) but do not send it |

Handlers:

- `control.prekey`: verify against the peer's IK; store it if its `id` differs
  and `created_at` is not older than the stored one.
- `control.stale_prekey`: store the prekey the same way, then put the outbox
  item `msg_id` back to `pending` so it is sealed again, but only if that item
  is addressed to this peer.
- `control.delivered`: delete the named outbox items addressed to this peer.
- `control.paused`: mark the peer "paused by peer" and hold its outbox.
- Control items are never confirmed; the sender deletes them from its outbox
  as soon as the relay has queued them (section 4.1).
- `control.resumed`: clear the mark and release the held outbox, unless this
  machine has paused the peer itself.
- `control.unpaired`: reject the peer's tasks awaiting approval, decline its
  held files, delete the peer, its keys and its outbox, deny it on the relay
  (on the next connection if offline), and write an `unpair` audit entry.

### 5.4 File offer

`file.offer`:

```json
{
  "file_id": "01J8ZS2N4P6R8T0V2X4Z6B8D0F",
  "blob_id": "opaque ID assigned by the relay",
  "name": "report.pdf",
  "size": 3148576,
  "chunks": 4,
  "sha256": "base64 of 32 bytes",
  "key": "base64 of 32 bytes",
  "task_id": "01J8ZR0A1B2C3D4E5F6G7H8J9K"
}
```

`task_id` is present when the file belongs to a task. Section 6 describes the
transfer.

## 6. Files

**Sender**

1. The path passes the outbound rules: a regular file inside the session's
   project folder or a folder added with `cravv-connect allow-path`, no
   component starting with `.`, not a secret-looking name (case-insensitive:
   `.env*`, `id_*`, `credentials*.json`, `service-account*.json`, or ending in
   `.pem`, `.key`, `.env`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.kdbx`, `.ppk`,
   `.ovpn`), symlinks resolved and checked again, the file opened without
   following symlinks and confirmed to be the file that was checked, exactly
   one hard link, at most 104857600 bytes. A peer this machine paused is
   refused.
2. Generate a random 32-byte key and a new `file_id`.
3. `POST /v1/blobs` with the recipient's IK, the size and
   `chunks = ceil(size / 1048576)` (1 for an empty file).
4. Encrypt chunk `i` with XChaCha20-Poly1305:
   - nonce: `SHA-256(file_id)[0:20] || uint32_be(i)` (24 bytes),
   - AAD: `file_id || uint32_be(i) || byte(last)` where `last` is 1 for the
     final chunk,
   - so a chunk moved to another index, another file, or a truncated file
     fails to decrypt.
5. Upload every chunk, hash the plaintext with SHA-256, write an audit entry,
   and send `file.offer` through the outbox.

**Receiver**

| Receiver's trust in sender | Result |
|---|---|
| chat-only | Held until a human runs `cravv-connect files accept <file_id>` (password). Accepting checks the peer again (still paired, not paused by this machine, trust still allows files). Pausing or unpairing the peer declines its held files |
| ask-first, autonomous | Downloaded at once |

The offer is checked first: `file_id`, `task_id` (when present) and the
message ID are IDs as in section 1, `blob_id` is 1 to 64 characters of
`[a-z0-9]`, the size is at most 104857600 bytes, `chunks` matches the
size, and `sha256` and `key` are 32 bytes each; otherwise it is dropped.
Before downloading, the receiver checks the per-peer quota (1 GiB by default,
counting files downloading now plus files downloaded within the last 30
days) and keeps 64 MiB of disk free on top of the file. A declined or failed offer creates a local inbox notice and sends
the sender a chat: `cravv-connect: file "<name>" (file_id <id>) was not
received: <reason>`.

The download writes `<target>.part`, one chunk at a time, recording the next
chunk index so a restart resumes where it stopped. After the last chunk it
checks the size and SHA-256 (on a mismatch it deletes the part file and the
next attempt starts again from chunk 0), then hard-links the part file to the target (it
never overwrites an existing file) and deletes the blob. A failed attempt is
retried after 2 seconds; after 3 attempts the offer is marked failed.
Turning the kill switch on stops running downloads; they continue from the
recorded chunk when the switch is turned off.

The target is `files/<alias>/<message id>-<name>` in the state directory,
where `<name>` is the peer's name reduced to its base name, with every
character outside `[A-Za-z0-9._-]` replaced by `_`, leading dots removed, at
most 100 characters, and `file` when nothing remains. The final path is
checked to be inside `files/`.

## 7. Prekey rotation and the stale prekey round trip

- The daemon creates its first prekey on start.
- Every minute a maintenance pass rotates the prekey once it is 7 days old
  (not while the kill switch is on): it stores a new prekey, marks every other
  one superseded, and sends `control.prekey` to each peer it has not paused.
- The same pass deletes private prekeys superseded more than 21 days ago. That
  covers the relay's 7-day queue plus 14 days of a sender retrying.
- A peer that missed the rotation (it was paused, or it was offline long
  enough for the old key to be purged) seals to a `pk_id` the receiver no
  longer has. The round trip:

```
A (sender)                         relay                      B (receiver)
seal(msg m1, pk_id=old) ---------> queue --------------------> Open: unknown pk_id
                                                               ack the delivery
                                   queue <-------------------- control.stale_prekey{msg_id: m1, prekey: current}
store B's current prekey  <------- deliver
outbox m1 -> pending
seal(m1, pk_id=current) ---------> queue --------------------> Open ok, dedup, handle
                                   queue <-------------------- control.delivered{ids:[m1]}
delete m1 from outbox     <------- deliver
```

The first copy never reaches the deduplication store under its message ID (it
could not be opened; only the once-per-message stale reply marker is stored),
so the re-sealed copy is handled exactly once. The e2e test
`TestStalePrekeyResend` exercises this path.

## 8. Pairing (pair-v1)

Pairing introduces two machines over a relay pairing room (`relay-v1.md`
section 5). The creator is side **A**, the joiner side **B**.

### 8.1 Bind code

`CRAVV-NNNN-SSSS-SSSS` in Crockford base32 (`0-9 A-Z` without `I L O U`):

- `NNNN` is the relay-assigned nameplate (the room ID the relay sees).
- `SSSS-SSSS` is the secret: 5 random bytes (40 bits). It never leaves the two
  machines.
- Input is case-insensitive; spaces and hyphens are ignored; `O` reads as `0`,
  `I` and `L` read as `1`; `U` is refused.

### 8.2 Setup

A (after `auth.unlock`):

1. requires a live, registered mailbox;
2. `invite_request` on its mailbox (single-use, 10 minutes);
3. `room_create`, which returns the nameplate and a creator token;
4. builds the code from the nameplate and a fresh secret;
5. opens `/v1/pair/{nameplate}?token=<creator_token>` and waits (at most 10
   minutes) for the joiner.

B (after `auth.unlock`) parses the code and opens `/v1/pair/{nameplate}`
without a token. B needs no mailbox yet.

### 8.3 Exchange

Both sides run the same steps. Each step sends one room message
(`{"t":"msg","data":"<base64>"}`) and then receives the peer's. Any failure
ends the exchange with "pairing failed", and closing the room burns it.

1. **SPAKE2.** Password: the normalized code string `CRAVV-NNNN-SSSS-SSSS`.
   Group: Ed25519 (the magic-wormhole and python-spake2 variant, `gospake2`).
   Identities: `cravv-connect/pair-v1/A` and `cravv-connect/pair-v1/B`. Each
   message is 33 bytes: the side byte `A` or `B` and a 32-byte group element.
   Both derive the 32-byte key `K`.
2. **Key confirmation.** Each side sends
   `HMAC-SHA256(key = HKDF-SHA256(ikm = K, salt = none, info = "cravv-connect/pair-v1/confirm", 32 bytes), msg = "A" or "B")`
   for its own side and checks the peer's tag in constant time. A wrong code
   fails here.
3. **Payload.** Each side sends its payload sealed with ChaCha20-Poly1305:
   - key: `HKDF-SHA256(ikm = K, salt = none, info = "cravv-connect/pair-v1/aead", 32 bytes)`,
   - nonce: 12 bytes, all zero except the last byte: `0` for A, `1` for B,
   - AAD: `cravv-connect/pair-v1/payload`,
   - plaintext:

   ```json
   {
     "ik": "base64 of the 32-byte Ed25519 identity key",
     "prekey": {"id": "...", "pub": "...", "created_at": 1790000000000, "sig": "..."},
     "relay_url": "https://relay.example.com",
     "name": "prith-mbp",
     "invite": "only from A: the single-use relay invite"
   }
   ```

4. **Validation.** The received IK is 32 bytes, is not this machine's own IK,
   the prekey signature verifies against it, and `relay_url` is an `https` URL
   (plain `http` only for localhost, private network addresses (RFC 1918, unique local IPv6, 100.64.0.0/10) and single-label `.local` names).
5. **Ack.** Each side sends the sealed constant `cravv-connect/pair-v1/ok`
   (same key and AAD, nonce last byte `2` for A, `3` for B) and checks the
   peer's. A payload tampered with in either direction therefore fails on both
   sides.

### 8.4 Finalize

Each side shows the human the peer's short machine ID and a suggested alias:
the peer's `name` lowercased, every run of characters outside `[a-z0-9]`
turned into one `-`, trimmed, at most 24 characters, or `peer-<6 characters of
the machine ID>` when nothing remains. The peer-chosen name is never shown
raw. The human picks the alias (1 to 24 characters of `a-z 0-9 -`, starting
with a letter or digit) and the trust level. `pair.finalize` then:

1. on a machine without a mailbox (the joiner), registers one with the invite
   from A's payload and waits for the connection;
2. stores the peer: IK, machine ID, alias, trust, prekey, relay URL;
3. adds the peer's IK to the relay allow-list;
4. queues `control.resumed` to the peer: the side that finalized first may
   already have sent and been told `not_allowed`, and this clears any pause it
   recorded and releases what it held;
5. writes an audit entry (`pair`, with the role `creator` or `joiner`).

A pending pairing lives 10 minutes; a successful exchange gets a fresh 10
minutes for the human to finalize. There is no fingerprint comparison step:
`cravv-connect peers` shows every machine ID for a later check.
