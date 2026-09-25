import { runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { REGISTRY_NAME } from "../src/registry";
import { ADMIN_TOKEN, testEnv } from "./helpers";

const registry = () => testEnv.REGISTRY.getByName(REGISTRY_NAME);
const GiB = 1024 * 1024 * 1024;

async function newInvite(issuer: string): Promise<string> {
  const r = await registry().createInvite(issuer);
  if (!r.ok) throw new Error(`createInvite: ${r.code}`);
  return r.invite;
}

function mid(n: number): string {
  return `m${String(n).padStart(51, "a")}`;
}

describe("registry", () => {
  it("registers with the admin token only when it matches exactly", async () => {
    expect(await registry().register(mid(1), "wrong")).toBe(false);
    expect(await registry().register(mid(1), ADMIN_TOKEN + "x")).toBe(false);
    expect(await registry().isMember(mid(1))).toBe(false);
    expect(await registry().register(mid(1), ADMIN_TOKEN)).toBe(true);
    expect(await registry().isMember(mid(1))).toBe(true);
    expect(await registry().register(mid(1))).toBe(true); // idempotent once a member
  });

  it("only members can create invites, and each invite works once", async () => {
    expect(await registry().createInvite(mid(2))).toEqual({ ok: false, code: "forbidden" });
    await registry().register(mid(2), ADMIN_TOKEN);
    const invite = await newInvite(mid(2));
    expect(await registry().register(mid(3), undefined, invite)).toBe(true);
    expect(await registry().register(mid(4), undefined, invite)).toBe(false);
  });

  it("records who invited each member", async () => {
    await registry().register(mid(9), ADMIN_TOKEN);
    const invite = await newInvite(mid(9));
    expect(await registry().register(mid(10), undefined, invite)).toBe(true);
    const rows = await runInDurableObject(registry(), (_inst, state) =>
      state.storage.sql
        .exec<{ mailbox_id: string; invited_by: string | null }>(
          "SELECT mailbox_id, invited_by FROM members WHERE mailbox_id IN (?, ?) ORDER BY mailbox_id",
          mid(9),
          mid(10),
        )
        .toArray(),
    );
    expect(rows).toEqual([
      { mailbox_id: mid(10), invited_by: mid(9) },
      { mailbox_id: mid(9), invited_by: null },
    ]);
  });

  it("rejects an expired invite", async () => {
    await registry().register(mid(5), ADMIN_TOKEN);
    const invite = await newInvite(mid(5));
    await runInDurableObject(registry(), (_inst, state) => {
      state.storage.sql.exec("UPDATE invites SET expires_at = ? WHERE token = ?", Date.now() - 1, invite);
    });
    expect(await registry().register(mid(6), undefined, invite)).toBe(false);
  });

  it("caps the relay-wide total of live blob bytes and frees it on release", async () => {
    const exp = Date.now() + 60_000;
    expect(await registry().reserveStorage("t1", 50 * GiB - 1, exp)).toBe(true);
    expect(await registry().reserveStorage("t2", 2, exp)).toBe(false);
    expect(await registry().reserveStorage("t2", 1, exp)).toBe(true);
    await registry().releaseStorage("t1");
    expect(await registry().reserveStorage("t3", GiB, exp)).toBe(true);
    await registry().releaseStorage("t2");
    await registry().releaseStorage("t3");
  });

  it("does not count expired storage reservations", async () => {
    expect(await registry().reserveStorage("t4", 50 * GiB, Date.now() - 1)).toBe(true);
    expect(await registry().reserveStorage("t5", GiB, Date.now() + 60_000)).toBe(true);
    await registry().releaseStorage("t5");
  });
});
