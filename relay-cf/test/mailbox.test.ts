import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { authMessage, b64encode } from "../src/crypto";
import { ADMIN_TOKEN, Conn, frameB64, handshake, Identity, member, type Msg, testEnv } from "./helpers";

async function pair(): Promise<{ a: Identity; b: Identity; ca: Conn; cb: Conn }> {
  const a = await Identity.create();
  const b = await Identity.create();
  const ca = await member(a);
  const cb = await member(b);
  expect((await cb.request({ t: "allow", ik: a.ikB64 })).status).toBe("ok");
  return { a, b, ca, cb };
}

describe("handshake", () => {
  it("sends welcome then challenge, and auth_ok with the 52-char mailbox id", async () => {
    const id = await Identity.create();
    const { conn, authOk } = await handshake(id);
    expect(authOk).toEqual({ t: "auth_ok", registered: false, mailbox_id: id.mailboxId });
    expect(id.mailboxId).toMatch(/^[a-z2-7]{52}$/);
    conn.close();
  });

  it("rejects an unsupported version", async () => {
    const id = await Identity.create();
    const conn = await Conn.open(`/v1/connect?ik=${encodeURIComponent(id.ikB64)}`);
    conn.send({ t: "hello", versions: [2] });
    expect(await conn.next()).toMatchObject({ t: "error", code: "unsupported_version" });
    await conn.waitClosed();
  });

  it("requires hello as the first message", async () => {
    const id = await Identity.create();
    const conn = await Conn.open(`/v1/connect?ik=${encodeURIComponent(id.ikB64)}`);
    conn.send({ t: "auth", ik: id.ikB64, sig: "" });
    expect(await conn.next()).toMatchObject({ t: "error", code: "bad_request" });
    await conn.waitClosed();
  });

  it("rejects a missing ik query parameter", async () => {
    const conn = await Conn.open("/v1/connect");
    expect(await conn.next()).toMatchObject({ t: "error", code: "bad_request" });
    await conn.waitClosed();
  });

  it.each([
    ["signature for another origin", { origin: "https://other.test" }],
    ["query ik of a different key", { queryIkOfOther: true }],
  ])("fails auth on %s", async (_name, opts) => {
    const id = await Identity.create();
    const other = await Identity.create();
    const { conn, authOk } = await handshake(id, {
      origin: "origin" in opts ? opts.origin : undefined,
      queryIk: "queryIkOfOther" in opts ? other.ikB64 : undefined,
    });
    expect(authOk).toMatchObject({ t: "error", code: "auth_failed" });
    await conn.waitClosed();
  });

  it("fails auth when the signature is garbage", async () => {
    const id = await Identity.create();
    const conn = await Conn.open(`/v1/connect?ik=${encodeURIComponent(id.ikB64)}`);
    conn.send({ t: "hello", versions: [1] });
    await conn.next();
    await conn.next();
    conn.send({ t: "auth", ik: id.ikB64, sig: b64encode(new Uint8Array(64)) });
    expect(await conn.next()).toMatchObject({ t: "error", code: "auth_failed" });
    await conn.waitClosed();
  });

  it("accepts a padded base64 ik in the query and auth frame", async () => {
    const id = await Identity.create();
    const padded = id.ikB64 + "=";
    const conn = await Conn.open(`/v1/connect?ik=${encodeURIComponent(padded)}`);
    conn.send({ t: "hello", versions: [1] });
    await conn.next();
    const ch = await conn.next();
    const sig = await id.sign(authMessage("https://relay.test", ch.nonce as string));
    conn.send({ t: "auth", ik: padded, sig: b64encode(sig) });
    expect(await conn.next()).toMatchObject({ t: "auth_ok", registered: false });
    conn.close();
  });
});

describe("registration", () => {
  it("only allows register while unregistered", async () => {
    const id = await Identity.create();
    const { conn } = await handshake(id);
    conn.send({ t: "allow", rid: "x", ik: id.ikB64 });
    expect(await conn.next()).toMatchObject({ t: "error", code: "not_registered" });
    await conn.waitClosed();
  });

  it.each([
    ["wrong admin token", { admin_token: "wrong" }],
    ["unknown invite", { invite: "nope" }],
    ["no credentials", {}],
  ])("refuses register with %s, then closes", async (_name, creds) => {
    const id = await Identity.create();
    const { conn } = await handshake(id);
    expect(await conn.request({ t: "register", ...creds })).toMatchObject({ status: "error", code: "forbidden" });
    await conn.waitClosed();
  });

  it("registers with the admin token and stays registered on reconnect", async () => {
    const id = await Identity.create();
    const first = await member(id, { admin_token: ADMIN_TOKEN });
    first.close();
    const { conn, authOk } = await handshake(id);
    expect(authOk).toMatchObject({ t: "auth_ok", registered: true });
    conn.close();
  });

  it("invites are single-use", async () => {
    const owner = await Identity.create();
    const oc = await member(owner);
    const res = await oc.request({ t: "invite_request" });
    expect(res.status).toBe("ok");
    const invite = res.invite as string;
    expect(invite.length).toBeGreaterThan(16);

    const b = await Identity.create();
    await member(b, { invite });
    const c = await Identity.create();
    const { conn } = await handshake(c);
    expect(await conn.request({ t: "register", invite })).toMatchObject({ status: "error", code: "forbidden" });
    await conn.waitClosed();
    oc.close();
  });
});

describe("invite cap", () => {
  it("allows 20 outstanding invites per member, then rate_limited", async () => {
    const owner = await Identity.create();
    const oc = await member(owner);
    for (let i = 0; i < 20; i++) expect((await oc.request({ t: "invite_request" })).status).toBe("ok");
    expect(await oc.request({ t: "invite_request" })).toMatchObject({ status: "error", code: "rate_limited" });
    await runInDurableObject(testEnv.REGISTRY.getByName("registry"), (_inst, state) => {
      state.storage.sql.exec("UPDATE invites SET expires_at = ? WHERE token IN (SELECT token FROM invites LIMIT 1)", Date.now() - 1);
    });
    expect((await oc.request({ t: "invite_request" })).status).toBe("ok");
    oc.close();
  });
});

describe("send, deliver, ack", () => {
  it("delivers live to an allowed recipient and replies queued", async () => {
    const { a, b, ca, cb } = await pair();
    const res = await ca.request({ t: "send", to: (await Identity.create()).mailboxId, id: "m0", frame: frameB64("x") });
    expect(res.status).toBe("unknown_mailbox");
    const sent = await ca.request({ t: "send", to: b.mailboxId, id: "m1", frame: frameB64("hello") });
    expect(sent.status).toBe("queued");
    const d = await cb.next();
    expect(d).toEqual({ t: "deliver", seq: 1, from: a.ikB64, id: "m1", frame: frameB64("hello") });
    ca.close();
    cb.close();
  });

  it("replies not_allowed when the sender is not on the allow-list, and after deny", async () => {
    const { a, b, ca, cb } = await pair();
    const x = await Identity.create();
    const cx = await member(x);
    expect((await cx.request({ t: "send", to: b.mailboxId, id: "n1", frame: frameB64("hi") })).status).toBe("not_allowed");
    expect((await cb.request({ t: "deny", ik: a.ikB64 })).status).toBe("ok");
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "n2", frame: frameB64("hi") })).status).toBe("not_allowed");
    ca.close();
    cb.close();
    cx.close();
  });

  it("replies too_large above 256 KiB of decoded frame", async () => {
    const { b, ca, cb } = await pair();
    const big = b64encode(new Uint8Array(262145));
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "big", frame: big })).status).toBe("too_large");
    const ok = b64encode(new Uint8Array(262144));
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "max", frame: ok })).status).toBe("queued");
    ca.close();
    cb.close();
  });

  it("replies too_large to an oversize frame string before decoding it", async () => {
    const { b, ca, cb } = await pair();
    // Not valid base64, but longer than any 256 KiB frame could encode to.
    const junk = "*".repeat(Math.ceil((262144 * 4) / 3) + 5);
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "junk", frame: junk })).status).toBe("too_large");
    const padded = b64encode(new Uint8Array(262144)) + "==";
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "pad", frame: padded })).status).toBe("queued");
    ca.close();
    cb.close();
  });

  it("redelivers unacked frames in seq order after reconnect, and seq is never reused", async () => {
    const { b, ca, cb } = await pair();
    cb.close();
    for (const id of ["r1", "r2", "r3"]) {
      expect((await ca.request({ t: "send", to: b.mailboxId, id, frame: frameB64(id) })).status).toBe("queued");
    }
    const c2 = await member(b);
    const got: Msg[] = [await c2.next(), await c2.next(), await c2.next()];
    expect(got.map((m) => [m.seq, m.id])).toEqual([
      [1, "r1"],
      [2, "r2"],
      [3, "r3"],
    ]);
    c2.send({ t: "ack", seq: 2 });
    c2.close();

    const c3 = await member(b);
    expect(await c3.next()).toMatchObject({ t: "deliver", seq: 3, id: "r3" });
    c3.send({ t: "ack", seq: 3 });
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "r4", frame: frameB64("r4") })).status).toBe("queued");
    expect(await c3.next()).toMatchObject({ t: "deliver", seq: 4, id: "r4" });
    c3.close();
    ca.close();
  });

  it("a new connection closes the older one with error gone", async () => {
    const id = await Identity.create();
    const first = await member(id);
    const second = await member(id);
    expect(await first.next()).toMatchObject({ t: "error", code: "gone" });
    await first.waitClosed();
    expect(second.closed).toBe(false);
    second.close();
  });

  it("replies queue_full at the frame cap (QUEUE_MAX_FRAMES=5 in tests)", async () => {
    const { b, ca, cb } = await pair();
    cb.close();
    for (let i = 0; i < 5; i++) {
      expect((await ca.request({ t: "send", to: b.mailboxId, id: `q${i}`, frame: frameB64("q") })).status).toBe("queued");
    }
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "q5", frame: frameB64("q") })).status).toBe("queue_full");
    ca.close();
  });

  it("drops frames older than the TTL at read time and in the alarm", async () => {
    const { b, ca, cb } = await pair();
    cb.close();
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "old", frame: frameB64("old") })).status).toBe("queued");
    const stub = testEnv.MAILBOX.getByName(b.mailboxId);
    await runInDurableObject(stub, (_inst, state) => {
      state.storage.sql.exec("UPDATE queue SET created_at = created_at - ?", 8 * 24 * 3600 * 1000);
    });
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "new", frame: frameB64("new") })).status).toBe("queued");
    const c2 = await member(b);
    expect(await c2.next()).toMatchObject({ t: "deliver", id: "new" });
    expect(await c2.quiet()).toBe(true);
    c2.close();

    await runInDurableObject(stub, (_inst, state) => {
      state.storage.sql.exec("UPDATE queue SET created_at = created_at - ?", 8 * 24 * 3600 * 1000);
    });
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    const left = await runInDurableObject(stub, (_inst, state) =>
      state.storage.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM queue").one().n,
    );
    expect(left).toBe(0);
    ca.close();
  });
});

describe("request handling", () => {
  it("answers an unknown type with a rid and keeps the connection", async () => {
    const id = await Identity.create();
    const c = await member(id);
    expect(await c.request({ t: "frobnicate" })).toMatchObject({ t: "res", status: "error", code: "bad_request" });
    expect((await c.request({ t: "invite_request" })).status).toBe("ok");
    c.close();
  });

  it.each([
    ["unknown type without rid", { t: "frobnicate" }],
    ["known type without rid", { t: "allow", ik: "AAAA" }],
    ["malformed ack", { t: "ack", seq: "x" }],
  ])("closes on %s", async (_name, frame) => {
    const id = await Identity.create();
    const c = await member(id);
    c.send(frame);
    expect(await c.next()).toMatchObject({ t: "error", code: "bad_request" });
    await c.waitClosed();
  });

  it.each([
    ["empty id", { id: "" }],
    ["id over 128 chars", { id: "x".repeat(129) }],
    ["frame not base64", { frame: "***" }],
    ["empty to", { to: "" }],
  ])("replies bad_request to send with %s", async (_name, patch) => {
    const { b, ca, cb } = await pair();
    const res = await ca.request({ t: "send", to: b.mailboxId, id: "ok", frame: frameB64("x"), ...patch });
    expect(res).toMatchObject({ status: "error", code: "bad_request" });
    ca.close();
    cb.close();
  });

  it("accepts a 128-char id", async () => {
    const { b, ca, cb } = await pair();
    const res = await ca.request({ t: "send", to: b.mailboxId, id: "x".repeat(128), frame: frameB64("x") });
    expect(res.status).toBe("queued");
    ca.close();
    cb.close();
  });

  it("checks the rate limit before validating the request", async () => {
    const id = await Identity.create();
    const c = await member(id);
    await runInDurableObject(testEnv.MAILBOX.getByName(id.mailboxId), (inst) => {
      const bucket = inst as unknown as { tokens: number; refilledAt: number };
      bucket.tokens = 0;
      bucket.refilledAt = Date.now() + 60_000;
    });
    expect((await c.request({ t: "send", to: "", id: "", frame: "***" })).status).toBe("rate_limited");
    c.send({ t: "ack", seq: 0 });
    expect(await c.quiet(200)).toBe(true);
    c.close();
  });

  it("keys the allow-list by sender mailbox id and it is directional", async () => {
    const { a, b, ca, cb } = await pair();
    expect((await ca.request({ t: "send", to: b.mailboxId, id: "ab", frame: frameB64("ab") })).status).toBe("queued");
    expect((await cb.request({ t: "send", to: a.mailboxId, id: "ba", frame: frameB64("ba") })).status).toBe("not_allowed");
    const rows = await runInDurableObject(testEnv.MAILBOX.getByName(b.mailboxId), (_inst, state) =>
      state.storage.sql.exec<{ mailbox_id: string }>("SELECT mailbox_id FROM allow").toArray(),
    );
    expect(rows).toEqual([{ mailbox_id: a.mailboxId }]);
    ca.close();
    cb.close();
  });
});

describe("internal errors", () => {
  it("sends a fixed internal error message, never exception details", async () => {
    const id = await Identity.create();
    const c = await member(id);
    await runInDurableObject(testEnv.MAILBOX.getByName(id.mailboxId), (_inst, state) => {
      state.storage.sql.exec("DROP TABLE allow");
    });
    c.send({ t: "allow", rid: "x", ik: id.ikB64 });
    expect(await c.next()).toEqual({ t: "error", code: "internal", message: "internal error" });
    await c.waitClosed();
  });
});
