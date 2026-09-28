import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { META_DDL, QUEUE_DDL } from "../src/queue";
import { ADMIN_TOKEN, Conn, frameB64, handshake, Identity, member, relayFetch, testEnv } from "./helpers";

type Named = { getByName(name: string): DurableObjectStub };

// The tables an object holds, and the size of its SQLite database. An object that never
// wrote anything has no tables.
async function storageOf(ns: Named, name: string): Promise<{ tables: string[]; size: number }> {
  return runInDurableObject(ns.getByName(name), (_inst, state: DurableObjectState) => ({
    tables: state.storage.sql
      .exec<{ name: string }>("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
      .toArray()
      .map((r) => r.name)
      .filter((n) => !n.startsWith("_cf_")),
    size: state.storage.sql.databaseSize,
  }));
}

// The database size of an object that was only instantiated, for comparison.
async function emptySize(): Promise<number> {
  return (await storageOf(testEnv.MAILBOX, `never-used-${crypto.randomUUID()}`)).size;
}

const enc = new TextEncoder();

async function signed(id: Identity, method: string, path: string, body: Uint8Array = new Uint8Array(0)): Promise<Response> {
  const headers = await id.signedHeaders(method, path, body);
  return relayFetch(path, { method, headers, body: method === "GET" || method === "DELETE" ? undefined : body });
}

async function registered(): Promise<Identity> {
  const id = await Identity.create();
  (await member(id)).close();
  return id;
}

describe("no storage without a registration, a room or a blob", () => {
  it("an unregistered key that connects, authenticates and is refused stores nothing", async () => {
    const id = await Identity.create();
    const { conn, authOk } = await handshake(id);
    expect(authOk).toMatchObject({ t: "auth_ok", registered: false });
    expect(await conn.request({ t: "register", admin_token: "wrong" })).toMatchObject({ status: "error", code: "forbidden" });
    await conn.waitClosed();
    expect(await storageOf(testEnv.MAILBOX, id.mailboxId)).toEqual({ tables: [], size: await emptySize() });
  });

  it("an unregistered key that sends another request stores nothing", async () => {
    const id = await Identity.create();
    const { conn } = await handshake(id);
    conn.send({ t: "send", rid: "1", to: id.mailboxId, id: "x", frame: frameB64("x") });
    expect(await conn.next()).toMatchObject({ t: "error", code: "not_registered" });
    await conn.waitClosed();
    expect((await storageOf(testEnv.MAILBOX, id.mailboxId)).tables).toEqual([]);
  });

  it("a send to an unregistered mailbox stores nothing there", async () => {
    const a = await Identity.create();
    const ca = await member(a);
    const nobody = await Identity.create();
    expect((await ca.request({ t: "send", to: nobody.mailboxId, id: "x", frame: frameB64("x") })).status).toBe("unknown_mailbox");
    ca.close();
    expect((await storageOf(testEnv.MAILBOX, nobody.mailboxId)).tables).toEqual([]);
  });

  it("a signed blob request from a non-member stores nothing in its mailbox", async () => {
    const outsider = await Identity.create();
    const down = await registered();
    const res = await signed(outsider, "POST", "/v1/blobs", enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 })));
    expect(res.status).toBe(403);
    expect((await signed(outsider, "GET", "/v1/blobs/aaaaaaaaaaaaaaaaaaaaaaaaaa/chunks/0")).status).toBe(403);
    expect(await storageOf(testEnv.MAILBOX, outsider.mailboxId)).toEqual({ tables: [], size: await emptySize() });
  });

  it("a member asking for an unknown blob stores nothing in that blob's object", async () => {
    const id = await registered();
    const blobId = "bbbbbbbbbbbbbbbbbbbbbbbbbb";
    expect((await signed(id, "GET", `/v1/blobs/${blobId}/chunks/0`)).status).toBe(404);
    expect((await signed(id, "DELETE", `/v1/blobs/${blobId}`)).status).toBe(404);
    expect((await storageOf(testEnv.BLOBMETA, blobId)).tables).toEqual([]);
  });

  it("a join on an unknown nameplate stores nothing", async () => {
    const nameplate = "ZZ9Z";
    for (const path of [`/v1/pair/${nameplate}`, `/v1/pair/${nameplate}?token=guess`]) {
      const c = await Conn.open(path);
      expect(await c.next()).toMatchObject({ t: "error", code: "not_found" });
      await c.waitClosed();
    }
    expect(await storageOf(testEnv.ROOM, nameplate)).toEqual({ tables: [], size: await emptySize() });
  });
});

describe("no tables left behind", () => {
  async function createRoom(): Promise<{ owner: Identity; nameplate: string; token: string }> {
    const owner = await Identity.create();
    const c = await member(owner);
    const res = await c.request({ t: "room_create" });
    c.close();
    expect(res.status).toBe("ok");
    return { owner, nameplate: res.nameplate as string, token: res.creator_token as string };
  }

  it("a room burned by a disconnect leaves no tables", async () => {
    const { nameplate, token } = await createRoom();
    expect((await storageOf(testEnv.ROOM, nameplate)).tables).toEqual(["owner", "pending", "room", "sqlite_sequence"]);
    const creator = await Conn.open(`/v1/pair/${nameplate}?token=${token}`);
    expect(await creator.next()).toEqual({ t: "waiting" });
    const joiner = await Conn.open(`/v1/pair/${nameplate}`);
    expect(await joiner.next()).toEqual({ t: "peer_joined" });
    joiner.close();
    await creator.waitClosed();
    await expect.poll(async () => (await storageOf(testEnv.ROOM, nameplate)).tables).toEqual([]);
  });

  it("an expired room leaves no tables", async () => {
    const { nameplate } = await createRoom();
    expect(await runDurableObjectAlarm(testEnv.ROOM.getByName(nameplate))).toBe(true);
    expect((await storageOf(testEnv.ROOM, nameplate)).tables).toEqual([]);
  });

  it("a deleted blob leaves no tables", async () => {
    const up = await registered();
    const down = await registered();
    const res = await signed(up, "POST", "/v1/blobs", enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 })));
    const { blob_id } = (await res.json()) as { blob_id: string };
    expect((await storageOf(testEnv.BLOBMETA, blob_id)).tables).toEqual(["blob", "chunk"]);
    expect((await signed(down, "DELETE", `/v1/blobs/${blob_id}`)).status).toBe(204);
    expect((await storageOf(testEnv.BLOBMETA, blob_id)).tables).toEqual([]);
  });

  it("an expired blob leaves no tables once its tombstone ends", async () => {
    const up = await registered();
    const down = await registered();
    const res = await signed(up, "POST", "/v1/blobs", enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 })));
    const { blob_id } = (await res.json()) as { blob_id: string };
    const stub = testEnv.BLOBMETA.getByName(blob_id);
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    expect((await storageOf(testEnv.BLOBMETA, blob_id)).tables).toEqual(["blob", "chunk"]);
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    expect((await storageOf(testEnv.BLOBMETA, blob_id)).tables).toEqual([]);
  });
});

describe("objects created by older versions", () => {
  it("a mailbox whose tables predate room_usage keeps working", async () => {
    const old = await Identity.create();
    const peer = await Identity.create();
    await testEnv.REGISTRY.getByName("registry").register(old.mailboxId, ADMIN_TOKEN);
    await runInDurableObject(testEnv.MAILBOX.getByName(old.mailboxId), (_inst, state) => {
      const sql = state.storage.sql;
      sql.exec(META_DDL);
      sql.exec(QUEUE_DDL);
      sql.exec("CREATE TABLE allow (mailbox_id TEXT PRIMARY KEY)");
      sql.exec("CREATE TABLE blob_usage (blob_id TEXT PRIMARY KEY, size INTEGER NOT NULL, expires_at INTEGER NOT NULL)");
      sql.exec("INSERT INTO meta (key, value) VALUES ('registered', '1')");
    });
    const { conn, authOk } = await handshake(old);
    expect(authOk).toMatchObject({ t: "auth_ok", registered: true });
    expect((await conn.request({ t: "room_create" })).status).toBe("ok");
    expect((await conn.request({ t: "allow", ik: peer.ikB64 })).status).toBe("ok");
    const cp = await member(peer);
    expect((await cp.request({ t: "send", to: old.mailboxId, id: "hi", frame: frameB64("hi") })).status).toBe("queued");
    expect(await conn.next()).toMatchObject({ t: "deliver", id: "hi" });
    conn.close();
    cp.close();
  });
});
