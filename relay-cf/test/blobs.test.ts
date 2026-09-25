import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { chunkKey } from "../src/blobmeta";
import { handleBlobs } from "../src/blobs";
import type { Env } from "../src/env";
import { REGISTRY_NAME } from "../src/registry";
import { Identity, member, ORIGIN, relayFetch, testEnv } from "./helpers";

const GiB = 1024 * 1024 * 1024;

// An R2 bucket whose put first runs `before`, to force a delete or expiry into the window
// between the chunk being recorded and its bytes landing in R2.
function racingBucket(before: () => Promise<void>): R2Bucket {
  return {
    put: async (key: string, value: Uint8Array) => {
      await before();
      return testEnv.BLOBS.put(key, value);
    },
    get: (key: string) => testEnv.BLOBS.get(key),
    delete: (keys: string | string[]) => testEnv.BLOBS.delete(keys),
  } as unknown as R2Bucket;
}

async function racedPut(up: Identity, blobId: string, before: () => Promise<void>): Promise<Response> {
  const path = `/v1/blobs/${blobId}/chunks/0`;
  const body = enc.encode("late");
  const headers = await up.signedHeaders("PUT", path, body);
  const env: Env = { ...testEnv, BLOBS: racingBucket(before) };
  return handleBlobs(new Request(`${ORIGIN}${path}`, { method: "PUT", headers, body }), env);
}

const enc = new TextEncoder();

async function signed(id: Identity, method: string, path: string, body: Uint8Array = new Uint8Array(0), ts?: number): Promise<Response> {
  const headers = await id.signedHeaders(method, path, body, ts);
  return relayFetch(path, { method, headers, body: method === "GET" || method === "DELETE" ? undefined : body });
}

async function registered(): Promise<Identity> {
  const id = await Identity.create();
  (await member(id)).close();
  return id;
}

async function createBlob(up: Identity, down: Identity, size = 5, chunks = 1): Promise<string> {
  const res = await signed(up, "POST", "/v1/blobs", enc.encode(JSON.stringify({ size, chunks, recipient: down.ikB64 })));
  expect(res.status).toBe(201);
  const { blob_id } = (await res.json()) as { blob_id: string };
  expect(blob_id).toMatch(/^[a-z2-7]{26}$/);
  return blob_id;
}

describe("blobs", () => {
  it("uploader puts, recipient gets, recipient deletes", async () => {
    const up = await registered();
    const down = await registered();
    const id = await createBlob(up, down);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/0`, enc.encode("hello"))).status).toBe(204);
    const got = await signed(down, "GET", `/v1/blobs/${id}/chunks/0`);
    expect(got.status).toBe(200);
    expect(new TextDecoder().decode(await got.arrayBuffer())).toBe("hello");
    expect((await signed(down, "DELETE", `/v1/blobs/${id}`)).status).toBe(204);
    expect((await signed(down, "GET", `/v1/blobs/${id}/chunks/0`)).status).toBe(404);
    expect(await testEnv.BLOBS.get(chunkKey(id, 0))).toBeNull();
  });

  it("enforces who may put, get and delete", async () => {
    const up = await registered();
    const down = await registered();
    const stranger = await registered();
    const id = await createBlob(up, down);
    expect((await signed(down, "PUT", `/v1/blobs/${id}/chunks/0`, enc.encode("x"))).status).toBe(403);
    expect((await signed(stranger, "PUT", `/v1/blobs/${id}/chunks/0`, enc.encode("x"))).status).toBe(403);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/0`, enc.encode("x"))).status).toBe(204);
    expect((await signed(up, "GET", `/v1/blobs/${id}/chunks/0`)).status).toBe(403);
    expect((await signed(stranger, "GET", `/v1/blobs/${id}/chunks/0`)).status).toBe(403);
    expect((await signed(stranger, "DELETE", `/v1/blobs/${id}`)).status).toBe(403);
    expect((await signed(up, "DELETE", `/v1/blobs/${id}`)).status).toBe(204);
  });

  it("rejects unregistered signers with 403 and bad signatures with 401", async () => {
    const down = await registered();
    const outsider = await Identity.create();
    const body = enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 }));
    const res = await signed(outsider, "POST", "/v1/blobs", body);
    expect(res.status).toBe(403);
    expect(await res.json()).toMatchObject({ code: "forbidden" });
    const up = await registered();
    const stale = Math.floor(Date.now() / 1000) - 301;
    expect((await signed(up, "POST", "/v1/blobs", body, stale)).status).toBe(401);
    const headers = await up.signedHeaders("POST", "/v1/blobs", body);
    const tampered = enc.encode(JSON.stringify({ size: 2, chunks: 1, recipient: down.ikB64 }));
    expect((await relayFetch("/v1/blobs", { method: "POST", headers, body: tampered })).status).toBe(401);
    expect((await relayFetch("/v1/blobs", { method: "POST", body })).status).toBe(401);
  });

  it("returns 413 for blobs over 100 MiB and oversize chunks", async () => {
    const up = await registered();
    const down = await registered();
    const tooBig = enc.encode(JSON.stringify({ size: 104857601, chunks: 101, recipient: down.ikB64 }));
    expect((await signed(up, "POST", "/v1/blobs", tooBig)).status).toBe(413);
    const id = await createBlob(up, down, 2 * 1048576, 2);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/0`, new Uint8Array(1048576 + 64))).status).toBe(204);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/1`, new Uint8Array(1048576 + 65))).status).toBe(413);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/2`, new Uint8Array(1))).status).toBe(400);
    const pad = enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64, pad: "x".repeat(4096) }));
    expect((await signed(up, "POST", "/v1/blobs", pad)).status).toBe(413);
  });

  it("refuses chunks beyond the declared size plus 64 bytes per chunk", async () => {
    const up = await registered();
    const down = await registered();
    const id = await createBlob(up, down, 10, 1);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/0`, new Uint8Array(75))).status).toBe(413);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/0`, new Uint8Array(74))).status).toBe(204);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/0`, new Uint8Array(20))).status).toBe(204);
  });

  it("checks chunk count on create and the chunk index on download", async () => {
    const up = await registered();
    const down = await registered();
    for (const [size, chunks] of [[10, 0], [10, 2], [1048577, 3]]) {
      const b = enc.encode(JSON.stringify({ size, chunks, recipient: down.ikB64 }));
      expect((await signed(up, "POST", "/v1/blobs", b)).status).toBe(400);
    }
    const id = await createBlob(up, down, 1048577, 2);
    expect((await signed(down, "GET", `/v1/blobs/${id}/chunks/2`)).status).toBe(400);
  });

  it("returns a JSON error body with a relay-v1 code", async () => {
    const up = await registered();
    const down = await registered();
    const id = await createBlob(up, down);
    const res = await signed(up, "GET", `/v1/blobs/${id}/chunks/0`);
    expect(res.status).toBe(403);
    expect(await res.json()).toEqual({ code: "forbidden", message: "only the recipient may read" });
  });

  it("returns 404 for unknown blobs and missing chunks", async () => {
    const up = await registered();
    const down = await registered();
    expect((await signed(down, "GET", `/v1/blobs/aaaaaaaaaaaaaaaaaaaaaaaaaa/chunks/0`)).status).toBe(404);
    const id = await createBlob(up, down, 10, 1);
    expect((await signed(down, "GET", `/v1/blobs/${id}/chunks/0`)).status).toBe(404);
  });

  it("returns 410 after the TTL alarm and removes the chunks", async () => {
    const up = await registered();
    const down = await registered();
    const id = await createBlob(up, down);
    expect((await signed(up, "PUT", `/v1/blobs/${id}/chunks/0`, enc.encode("bye"))).status).toBe(204);
    expect(await runDurableObjectAlarm(testEnv.BLOBMETA.getByName(id))).toBe(true);
    expect((await signed(down, "GET", `/v1/blobs/${id}/chunks/0`)).status).toBe(410);
    expect(await testEnv.BLOBS.get(chunkKey(id, 0))).toBeNull();
  });

  it("stops reading an oversize body without Content-Length at the limit", async () => {
    let sent = 0;
    const endless = () =>
      new ReadableStream<Uint8Array>({
        pull(controller) {
          sent += 65536;
          controller.enqueue(new Uint8Array(65536));
        },
      });
    for (const [method, path] of [["PUT", "/v1/blobs/aaaaaaaaaaaaaaaaaaaaaaaaaa/chunks/0"], ["POST", "/v1/blobs"]]) {
      sent = 0;
      const res = await relayFetch(path, { method, body: endless() });
      expect(res.status).toBe(413);
      expect(await res.json()).toMatchObject({ code: "too_large" });
      expect(sent).toBeLessThan(8 * 1048576);
    }
  }, 5000);

  it("a chunk PUT racing a delete removes its own R2 object and answers 404", async () => {
    const up = await registered();
    const down = await registered();
    const id = await createBlob(up, down);
    const res = await racedPut(up, id, async () => {
      expect((await signed(down, "DELETE", `/v1/blobs/${id}`)).status).toBe(204);
    });
    expect(res.status).toBe(404);
    expect(await res.json()).toMatchObject({ code: "not_found" });
    expect(await testEnv.BLOBS.get(chunkKey(id, 0))).toBeNull();
  });

  it("a chunk PUT racing the TTL alarm removes its own R2 object and answers 410", async () => {
    const up = await registered();
    const down = await registered();
    const id = await createBlob(up, down);
    const res = await racedPut(up, id, async () => {
      expect(await runDurableObjectAlarm(testEnv.BLOBMETA.getByName(id))).toBe(true);
    });
    expect(res.status).toBe(410);
    expect(await res.json()).toMatchObject({ code: "gone" });
    expect(await testEnv.BLOBS.get(chunkKey(id, 0))).toBeNull();
  });

  it("checks blob membership in the caller's own mailbox, not the registry", async () => {
    const up = await registered();
    const down = await registered();
    // Registry says member, mailbox flag says not: the mailbox wins.
    await runInDurableObject(testEnv.MAILBOX.getByName(up.mailboxId), (_inst, state) => {
      state.storage.sql.exec("DELETE FROM meta WHERE key = 'registered'");
    });
    expect(await testEnv.REGISTRY.getByName(REGISTRY_NAME).isMember(up.mailboxId)).toBe(true);
    const body = enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 }));
    expect((await signed(up, "POST", "/v1/blobs", body)).status).toBe(403);
  });

  it("enforces the per-member quota in the member's mailbox and frees it on delete", async () => {
    const up = await registered();
    const down = await registered();
    const mailbox = testEnv.MAILBOX.getByName(up.mailboxId);
    const exp = Date.now() + 60_000;
    expect(await mailbox.reserveBlob("q1", GiB, exp)).toBe("ok");
    expect(await mailbox.reserveBlob("q2", GiB - 5, exp)).toBe("ok");
    const id = await createBlob(up, down, 5, 1);
    const body = enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 }));
    const full = await signed(up, "POST", "/v1/blobs", body);
    expect(full.status).toBe(413);
    expect(await full.json()).toMatchObject({ code: "too_large" });
    expect((await signed(up, "DELETE", `/v1/blobs/${id}`)).status).toBe(204);
    expect((await signed(up, "POST", "/v1/blobs", body)).status).toBe(201);
  });

  it("caps live blobs per member at 256 with 413 too_large", async () => {
    const up = await registered();
    const down = await registered();
    const exp = Date.now() + 60_000;
    await runInDurableObject(testEnv.MAILBOX.getByName(up.mailboxId), (_inst, state) => {
      for (let i = 0; i < 255; i++) {
        state.storage.sql.exec("INSERT INTO blob_usage (blob_id, size, expires_at) VALUES (?, 1, ?)", `seed${i}`, exp);
      }
    });
    await createBlob(up, down, 1, 1);
    const body = enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 }));
    const res = await signed(up, "POST", "/v1/blobs", body);
    expect(res.status).toBe(413);
    expect(await res.json()).toEqual({ code: "too_large", message: "too many live blobs" });
  });

  it("refuses a blob when the relay-wide storage total is reached, and releases the member quota", async () => {
    const up = await registered();
    const down = await registered();
    const registry = testEnv.REGISTRY.getByName(REGISTRY_NAME);
    const used = await runInDurableObject(registry, (_inst, state) =>
      state.storage.sql.exec<{ used: number }>("SELECT COALESCE(SUM(size), 0) AS used FROM blob_storage").one().used,
    );
    expect(await registry.reserveStorage("fill", 50 * GiB - used, Date.now() + 60_000)).toBe(true);
    try {
      const body = enc.encode(JSON.stringify({ size: 1, chunks: 1, recipient: down.ikB64 }));
      const res = await signed(up, "POST", "/v1/blobs", body);
      expect(res.status).toBe(413);
      expect(await res.json()).toMatchObject({ code: "too_large" });
      const left = await runInDurableObject(testEnv.MAILBOX.getByName(up.mailboxId), (_inst, state) =>
        state.storage.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM blob_usage").one().n,
      );
      expect(left).toBe(0);
    } finally {
      await registry.releaseStorage("fill");
    }
  });
});
