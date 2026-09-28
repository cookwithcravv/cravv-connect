# peer-v1: end-to-end messages between cravv-connect machines

Status: normative. Frame and envelope version 1, protocol version 2.

peer-v1 is what two paired machines say to each other. The name is the
wire format: the frame, the envelope (`"v": 1`) and the sealing label
`cravv-connect/peer-v1` are unchanged since the first release, so this
file is updated in place rather than renamed. Protocol version 2 (session
links) changed what travels inside: chat, tasks and files now carry a
`link_id` and are accepted only on an accepted link between two sessions
(section 5.6); discovery, link and presence kinds were added (sections 5.5
to 5.7); per-machine trust levels were removed. A machine that still sends
link-less (version 1) traffic gets `control.unsupported{min_version: 2}`
(section 5.3). Every message travels
through a relay (see `relay-v1.md`) as an opaque frame: the relay sees the
sender's identity key, the recipient mailbox, a message ID, the frame size and
the time. For files it also sees the recipient, the size and the chunk
count, and for pairing the nameplate. It never sees message contents. This document covers:

1. identities and prekeys,
2. the sealed frame format,
3. how a receiver opens, checks and confirms frames,
4. every message kind and its body, including sessions, links and presence,
5. prekey rotation and the stale prekey round trip,
6. file transfer,
7. the pairing protocol that introduces two machines.

Go reference: `internal/core` (envelope, bodies, kind traits, limits),
`internal/keys`, `internal/sealing`, `internal/filecrypt`, `internal/pake`,
`internal/bindcode`, `internal/pathguard`, and `internal/daemon` (outbound,
inbound, handlers, linkgate, links, links_offer, discovery, presence,
linkreplies, versions, pairing, files, tasks, prekeys, peers, killswitch).

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
  "to_machine": "k3d...52 chars",
  "link_id": "01J8ZQ3W5TAV9M2C4XKQ7N6B1D",
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
| `link_id` | The link the message travels on. Required on `chat`, `task.create`, `task.update`, `task.cancel` and `file.offer`; omitted on every other kind |
| `kind` | See section 5 |
| `body` | JSON object whose shape depends on `kind` |

The envelope names no session. The receiver takes the sending session and
the local session from its own record of the link (section 5.6), so a peer
cannot address or impersonate a session by writing its name.

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
      ack the delivery, and stop. The reply is sent only when `h.id` is a valid
      message ID (section 1) whose millisecond time passes the freshness check
      of step 5 (otherwise the frame is dropped), at most once per sender and
      message ID (recorded in the deduplication store), so a relay redelivery
      of the same frame does not trigger another resend, and at most 10 times
      per sender per minute (a frame over that gets no reply; its sender
      resends it after the relay TTL);
   6. decrypt;
   7. the envelope has `v` 1 and its `id`, `from_machine`, `to_machine` equal the
      header's. This stops a valid signature from being stripped and replaced.
5. **Freshness.** Drop the message if `ts` is more than 21 days in the past or
   more than 10 minutes in the future. Future timestamps are counted, and
   `status` warns about clock skew. An ephemeral kind (`sessions.list`,
   `sessions.listed`, `presence.ping`, `presence.pong`) is then handled at
   once and never deduplicated, recorded or confirmed; its handler drops it
   when it is older than 120 seconds, so frames the relay queued while this
   machine was offline expire harmlessly.
6. **Replay.** If `id` is already in the deduplication store (kept 30 days),
   do not handle it again; for non-control kinds, confirm it again with
   `control.delivered` (the sender may have missed the first receipt).
7. **Handle** it through the handler registered for `kind`. Kinds with no
   handler are dropped. `chat`, `task.*` and `file.offer` pass through the
   link gate first, the single enforcement point for link traffic. It
   admits the message only when:
   1. it carries a `link_id`; otherwise it is dropped and answered with
      `control.unsupported` (at most once per peer per hour);
   2. the link keyed by (sender machine, `link_id`) is `active` here and its
      local session is open or away; otherwise it is dropped and answered
      with `link.closed{unknown_link}` (at most once per link per minute);
   3. the link's `permission_in` allows the kind (section 5.2); a
      `task.create` it does not allow is recorded as rejected and the
      sender is told.

   The handler then works on that link's local session only, and takes the
   peer's session from the link record. `chat` and `task.update` also
   pass the link's inbound limits: at most 60 a minute (a token bucket of
   120), and at most 1000 items or 32 MiB of bodies the local session has
   not read yet. An item over either is dropped as a handler failure for
   good: it is recorded and confirmed (steps 8 and 9), so the sender stops
   resending it.
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

Dropped frames are logged. `status` counts them apart: frames that are
unparseable, unverifiable or have a bad ID or timestamp ("corrupt or
unverifiable"), frames from unknown machines, and frames from machines this
one paused. Stale ephemeral frames, expected after being offline, and kinds
without a handler are only logged. A handler failure that is
worth retrying (a local database error, including a failed deduplication
lookup) is not acked: the receiver stops reading, closes the connection and
reconnects after a backoff (1 second, doubling, at most 5 minutes), and
because acks are cumulative the relay redelivers from that frame. Every other handler failure is logged,
recorded, confirmed and acked, so the sender stops resending an item that
can never be used.

Receipts are batched per peer. The batch is sent when a burst of deliveries
ends and at least every 200 ms.

### 4.1 Sending and the outbox

Every outgoing message goes through the persistent outbox except
`control.paused`, `control.unpaired`, the replies `control.unsupported` and
`link.closed{unknown_link}`, and the ephemeral kinds (`sessions.*`,
`presence.*`): those are sent once, directly, when the relay connection is
up, and never retried. The item stores the envelope, not the frame, and
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
| `not_allowed` | Within 30 minutes of pairing: the peer has most likely not finalized yet (it has not allowed this machine on the relay); retry with backoff capped at 5 seconds and mark nothing. Later: the peer paused (or unpaired) this machine: treat it like `control.paused` (mark the peer "paused by peer", hold every non-control item for it until `control.resumed`, and close its links). Control items keep retrying with backoff |
| `too_large` | Drop the item and report it in `status` (items for a peer that is no longer paired are dropped the same way) |
| `unknown_mailbox` | The peer has no mailbox on this relay yet (it just paired or set up again): back off from 1 second, doubling per attempt, at most 30 minutes, and report in `status` how many items wait for it. Any other answer for that peer retries its waiting items at once. An item still waiting when it is 21 days old is dropped and reported |
| `queue_full`, `rate_limited`, `error` with code `internal`, network error | Back off: 1 second, doubling per attempt, at most 5 minutes. An `internal` answer fails that request only: the connection stays up |

`control.delivered` deletes the confirmed items (only items addressed to the
peer that sent the receipt). Items older than 21 days are purged. Receivers
never confirm control kinds, so a control item leaves the outbox as soon as
the relay has queued it; the relay's at-least-once delivery covers it from
there.

`control.paused` and `control.unpaired` are sent once, directly, when the
relay connection is up (best effort), because the peer record changes right
after. Sending on a link that is not `active` fails locally at once; nothing
is queued for a closed link.

## 5. Kinds

Size limits: chat text, task instructions, task results and notes are at most
65536 bytes each.

### 5.1 Chat

`chat`: `{"text": "..."}`

```json
{"text": "tests pass on the GPU box, pushing now"}
```

Delivered on any active link, to the link's local session only. While that
session is away the item waits for it; if the link closes first, what it
has not read from that link is dropped.

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
`task_id` on the same link. The receiver applies its link's `permission_in`:

| `permission_in` on the receiver | Result |
|---|---|
| `messages` | `rejected`, update note `not permitted` |
| `tasks-ask` | `awaiting_approval` until a human on the receiver approves (then `queued`) or denies (`rejected`, note `denied by the receiving human`); expires after 24 hours (`expired`). The session gets an approval notice without the instructions |
| `tasks-auto` | `queued`; unclaimed after 24 hours it becomes `expired` |

A `task.create` whose `task_id` is already known is ignored (a redelivery of
the message that created a queued task only completes a delivery that an
earlier attempt left unfinished).

The receiver reports held and refused tasks with a `task.update`
(`awaiting_approval`, then `queued` after approval, or `rejected`). The
first time the receiving session's inbox returns a queued task, the
receiver sends `task.update` with state `seen` (the task stays `queued`
there), so the sender can tell a session that is slow from one that never
looked. Approval is a human decision on the receiver: in the chat
(`review_pending`, an elicitation form or a confirmation code), or with the
password (`cravv-connect approvals`, the web UI). Approving checks the link
again: it is refused when the link closed or no longer allows tasks.
Lowering a link to `messages` rejects its tasks still waiting (for approval
or a claim); pausing or unpairing the peer closes its links.

`task.update` (receiver to sender):

```json
{"task_id": "01J8ZR0A1B2C3D4E5F6G7H8J9K", "state": "done", "result": "F1 = 0.913", "files": []}
```

| Field | Meaning |
|---|---|
| `state` | `awaiting_approval`, `queued`, `seen`, `claimed`, `running`, `done`, `failed`, `cancelled`, `rejected`, or `expired` |
| `note` | Progress note or reason (`not permitted`, `killed`, `link_closed`, `rate_limited`, ...) |
| `result` | Final result text (with `done`) |
| `files` | Result files, sent as `file.offer` with the same `task_id` |

The sender only accepts updates for its own outbound tasks from the peer
the task was sent to, on the task's link, and never moves a task
backwards: the order is `sent` < `awaiting_approval` < `queued` < `seen` <
`claimed` < `running` < every terminal state. An update ranked lower than the current
state still adds its note, result and files. Updates after a terminal state
are ignored, and an update with state `sent` or an unknown state is rejected.

Receiver-side transitions:

```
awaiting_approval -> queued | rejected | expired | cancelled | failed
queued            -> claimed | expired | cancelled | rejected | failed
claimed           -> running | done | failed | cancelled
running           -> done | failed | cancelled
```

Only the link's local session can claim the task, atomically. The kill
switch fails claimed and running tasks with note `killed`. When a link
closes, every unfinished task on it fails with note `link_closed` on both
sides: the receiver tells the sender when it can, and the sender also fails
its own copy when its side of the link closes, so neither waits for an
update that cannot come. A managed session fails a task it refuses to run
with `rate_limited` or `folder_refused`, and one its run did not finish
with the reason the run ended (`no_result`, `run_timeout`, `run_failed: ...`,
`stopped`, `interrupted`).

`task.cancel` (sender to receiver): `{"task_id": "..."}`. Only the session
that created the task can cancel it, and the sender marks its copy
`cancelled` before sending. The receiver accepts it only from the peer that
sent the task: an `awaiting_approval`, `queued`, `claimed` or `running` task
becomes `cancelled` with note `cancelled by sender`, and the claiming session
(or the link's session, if unclaimed) gets a `task.update` inbox item with
that note. No `task.update` is sent back; unknown or finished tasks are ignored.

### 5.3 Control

| Kind | Body | Sent when |
|---|---|---|
| `control.prekey` | `{"prekey": signed prekey}` | After a rotation, to every peer this machine has not paused |
| `control.stale_prekey` | `{"msg_id": "...", "prekey": signed prekey}` | A frame arrived sealed to an unknown or purged prekey (once per sender and message ID) |
| `control.delivered` | `{"ids": ["...", "..."]}` | Non-control messages were stored (4, step 8) |
| `control.paused` | `{}` | This machine paused the peer (sent directly, then the peer is denied on the relay) |
| `control.resumed` | `{}` | This machine resumed the peer, or finalized pairing with it |
| `control.unpaired` | `{}` | This machine unpaired the peer (sent directly, best effort) |
| `control.relay_moved` | `{"relay_url": "https://..."}` | Reserved for relay changes. Daemons accept and store it (only `https` URLs, or `http` for localhost and private network addresses) but do not send it |
| `control.unsupported` | `{"min_version": 2}` | A peer sent `chat`, `task.*` or `file.offer` without a `link_id` (version 1). Sent directly, at most once per peer per hour; the message itself is dropped |

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
- `control.unpaired`: close every link with the peer, reject its tasks
  awaiting approval, decline its held files, delete the peer, its keys and
  its outbox, deny it on the relay (on the next connection if offline), and
  write an `unpair` audit entry. `control.paused` also closes every link
  with the peer.
- `control.unsupported`: `status` shows that the peer needs this machine to
  upgrade (when `min_version` is higher than this machine's protocol
  version).

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

`task_id` is present when the file belongs to a task. A file travels on a
link like chat (`link_id` in the envelope) and every active link may carry
files. Section 6 describes the transfer.

### 5.5 Sessions and discovery

A session is a named endpoint on one machine (`<alias>/<name>`): a chat
that shared itself, or a managed session the daemon started. Pairing lets
a machine discover the sessions the other one shows it and ask for links;
it grants nothing else.

`sessions.list`: `{"req_id": "<ID>"}`

`sessions.listed`:

```json
{
  "req_id": "01J8ZT0...",
  "sessions": [{"session_id": "01J8ZT1...", "name": "trainer", "purpose": "fine-tunes the wake word model",
                "kind": "live", "agent": "claude", "state": "open"}],
  "offers": [{"offer_id": "01J8ZT2...", "label": "trainer", "agent": "claude", "max_permission": "tasks-auto"}]
}
```

- Both are ephemeral: sent directly, never confirmed or retried, dropped
  when older than 120 seconds. The asker matches the answer by `req_id` and
  waits at most 10 seconds.
- The receiver answers at most 30 lists per peer per minute and lists only
  its open and away sessions whose visibility includes the asker
  (`private`, `all-peers`, or named peers), plus the managed-session offers
  its owner made to the asker (labels only: never the folder or the
  limits). The project folder is never sent.
- `name` is 1 to 32 characters of `[a-z0-9-]`, not starting with `-`;
  `purpose` one line of at most 120 characters; `kind` `live` or `managed`;
  `state` `open` or `away`. The asker drops entries that break these rules
  and shows the purpose only inside a `<remote_message>` wrapper.

### 5.6 Links

A link connects one session on each machine. The requester mints its
`link_id` (an ID as in section 1); each side stores the link keyed by
(remote machine, `link_id`) with its own local number, and derives the
peer's session only from that record. Each side sets `permission_in`, what
the other side may do to it: `messages` (chat and files), `tasks-ask`
(also tasks that each need a human decision on this side) or `tasks-auto`
(also tasks its agent may carry out without asking).

| Kind | Body |
|---|---|
| `link.request` | `{"link_id", "from_session": {"id", "name", "purpose"}, "to_session_id" or "offer_id", "proposed_permission", "note"}` |
| `link.accepted` | `{"link_id", "to_session": {"id", "name", "purpose"}, "granted_permission"}` |
| `link.rejected` | `{"link_id", "reason"}`: `declined`, `not_found`, `busy`, `policy` or `timeout` |
| `link.closed` | `{"link_id", "reason"}`: `closed_by_peer`, `session_closed`, `paused`, `unpaired`, `killed`, `presence_timeout` or `unknown_link` |
| `link.state` | `{"link_id", "state": "active" or "away", "permission_in"}` |

All five travel through the outbox and are confirmed like chat, except the
`link.closed{unknown_link}` reply (sent directly).

**Request.** `proposed_permission` is what the requester would like to do
on the other side; `note` is at most 280 characters. The requester's own
side of the new link lets the peer send messages only. The receiver:

1. ignores a `link_id` it already has (a redelivery);
2. rejects `busy` past 10 new requests from the peer in a minute or while 5
   requests from it are pending, `policy` for a malformed request, and
   `timeout` for one sent more than 10 minutes ago;
3. rejects `not_found` for a session that is missing, closed or not visible
   to the requester, and for an unknown offer. These answers are identical,
   so a peer cannot probe for private sessions, and nothing is stored;
4. otherwise stores the request as `pending`, tells the target session (an
   inbox notice and the listener) and shows a desktop notification.

A pending request is decided once, on the receiving side, by a human:
accepted (at the level asked or lower) or rejected (`declined`).
Accepting at `messages` or `tasks-ask` takes a chat decision or the
password; accepting at `tasks-auto` takes the password. A request pending
for 10 minutes is rejected with `timeout`. On the sending side, a request
whose `link.request` is still waiting for the peer to have a relay mailbox
(section 4.1, `unknown_mailbox`) when it times out is taken out of the
outbox, and the session is told `not sent: <alias> has no mailbox on the
relay yet` instead.

**Request to an offer.** With `offer_id` instead of `to_session_id`, the
receiver checks the offer's rules, caps and concurrency limit, creates a
managed session named after the offer's label plus a 4-character suffix,
and answers `link.accepted` at once with `granted_permission` the lower of
the proposed level and the offer's permission, except that `tasks-ask`
becomes `messages` (a managed session has nobody to ask). The owner's
password-gated offer was the approval. It rejects `not_found` for an offer that does not
exist or was made to another machine, `busy` at a limit, and `policy` when
the offer's folder no longer passes its checks.

**Accepted.** The requester activates its pending link and records the
acceptor's session (`to_session`) and `granted_permission` (what it may do
there). An answer for a link that is not pending there gets
`link.closed{unknown_link}`.

**State.** Each side sends `link.state` when its session goes away or
comes back and when it changes `permission_in`. It is informational: each
side enforces only its own `permission_in`. Lowering needs nothing;
raising, or accepting at `tasks-auto`, needs the password.

**Close.** A link closes when either session closes, either side
disconnects it, on pause or unpair of the machine, on the kill switch, or
when it stays away after a presence timeout for its grace (section 5.7),
and a closed link never reopens. The side that closes
sends `link.closed` (except on pause and unpair, where `control.paused` and
`control.unpaired` already say so) and tells its session. The receiver of
`link.closed` closes its side and tells its session; it never answers a
`link.closed`, so two sides that both lost a link cannot bounce replies.
Closing fails the link's unfinished tasks (section 5.2), stops its file
downloads and declines its held files, drops what an away session had not
read from the link, and closes a managed session whose link it was.

### 5.7 Presence

| Kind | Body |
|---|---|
| `presence.ping` | `{"ts": 1790000123456, "link_ids": ["..."]}` |
| `presence.pong` | `{"ts": 1790000123456, "link_ids_open": ["..."]}` |

- While a peer has at least one active link, each side pings it every 30
  seconds, naming those links (at most 1000).
- The receiver answers with the named links that are open on its side
  (`pending` counts as open, because its `link.accepted` may not have
  arrived yet), echoing `ts`. A ping is also fresh evidence for the links
  it names.
- A link without fresh evidence (a pong, a ping or link traffic) for 150
  seconds is marked away on that side (nothing is sent). It is still
  pinged, and a fresh pong or traffic on it makes it active again.
- An away link closes with `presence_timeout` when it stays away for its
  grace: 10 minutes for a live session's link, the managed session's idle
  timeout for a managed one. `link.closed` is queued, so the peer
  converges when it is reachable again.
- A fresh pong to a recent ping that leaves a link out closes it at once
  (`presence_timeout`): the peer no longer has it.
- A side that finds more than 60 seconds passed since its last heartbeat
  round (it slept) resets its evidence, pings, and closes nothing in that
  round.
- Only silence after a ping that left counts. While a side's own relay
  mailbox is not live, and for a peer whose ping of the last round could
  not be sent, that side treats the round like a sleep for those links: it
  resets their evidence and marks or closes nothing. A machine that is
  itself offline therefore never times out its links.
- Both kinds are ephemeral (section 4, step 5): a ping or pong older than
  120 seconds, or more than 10 minutes in the future, is ignored.

A session that closes sends `link.closed{session_closed}` on each of its
links at once, through the outbox, so the peer learns within seconds while
both machines are online. When a machine drops, the other side marks its
links away within 150 seconds and closes them after the grace.

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

A `file.offer` on an active link is downloaded at once, for the link's
session. (Files held for a human, accepted with `cravv-connect files
accept <file_id>`, exist only from before the upgrade to protocol version
2; pausing or unpairing the peer, or closing the link, declines them.)

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
- A peer that seals to a `pk_id` the receiver still has but has replaced
  missed the `control.prekey` (a control item leaves the outbox once the
  relay queued it, and the relay keeps it only 7 days). The receiver handles
  the frame and sends that peer `control.prekey` with its current prekey
  again, at most once per peer every 10 minutes.
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
     "name": "alice-mbp",
     "invite": "only from A: the single-use relay invite",
     "bind_sig": "base64 of the 64-byte Ed25519 signature by ik over the binding transcript"
   }
   ```

   The binding transcript proves the sender holds `ik` in this very
   exchange, so a machine that knows the code cannot present another
   machine's identity key and prekey as its own (an unknown key share). For
   the sender on side S (`A` or `B`) and the other side O it is the bytes

   ```
   "cravv-connect/pair-v1/bind\n" || S || O || HKDF-SHA256(ikm = K, salt = none, info = "cravv-connect/pair-v1/bind", 32 bytes)
   ```

   where `\n` is the byte 0x0a and S and O are the single bytes `A` or `B`.

4. **Validation.** The received IK is 32 bytes, is not this machine's own IK,
   `bind_sig` verifies against it over the transcript for the peer's side
   (A checks S = `B`, O = `A`; B checks S = `A`, O = `B`), the prekey
   signature verifies against it, and `relay_url` is an `https` URL
   (plain `http` only for localhost, private network addresses (RFC 1918, unique local IPv6, 100.64.0.0/10) and single-label `.local` names).
   A payload without `bind_sig` comes from cravv-connect 0.2.1 or older: it
   is refused, and the human is told to update cravv-connect on both
   machines and pair again.
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
with a letter or digit). `pair.finalize` then:

1. on a machine without a mailbox (the joiner), registers one with the invite
   from A's payload and waits for the connection;
2. stores the peer: IK, machine ID, alias, prekey, relay URL. The peer gets
   no links: it can discover sessions and ask for links, and each link is
   decided on its own (section 5.6);
3. adds the peer's IK to the relay allow-list;
4. queues `control.resumed` to the peer: the side that finalized first may
   already have sent and been told `not_allowed`, and this clears any pause it
   recorded and releases what it held;
5. writes an audit entry (`pair`, with the role `creator` or `joiner`).

A pending pairing lives 10 minutes; a successful exchange gets a fresh 10
minutes for the human to finalize. There is no fingerprint comparison step:
`cravv-connect peers` shows every machine ID for a later check.
