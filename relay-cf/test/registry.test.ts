import { runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { REGISTRY_NAME } from "../src/registry";
import { ADMIN_TOKEN, testEnv } from "./helpers";

const registry = () => testEnv.REGISTRY.getByName(REGISTRY_NAME);
const GiB = 1024 * 1024 * 1024;

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
    expect(await registry().createInvite(mid(2))).toBeNull();
    await registry().register(mid(2), ADMIN_TOKEN);
    const invite = await registry().createInvite(mid(2));
    expect(invite).not.toBeNull();
    expect(await registry().register(mid(3), undefined, invite as string)).toBe(true);
    expect(await registry().register(mid(4), undefined, invite as string)).toBe(false);
  });

  it("rejects an expired invite", async () => {
    await registry().register(mid(5), ADMIN_TOKEN);
    const invite = (await registry().createInvite(mid(5))) as string;
    await runInDurableObject(registry(), (_inst, state) => {
      state.storage.sql.exec("UPDATE invites SET expires_at = ? WHERE token = ?", Date.now() - 1, invite);
    });
    expect(await registry().register(mid(6), undefined, invite)).toBe(false);
  });

  it("enforces the per-member blob quota and frees it on release", async () => {
    const exp = Date.now() + 60_000;
    expect(await registry().reserveBlob(mid(7), "b1", GiB, exp)).toBe(true);
    expect(await registry().reserveBlob(mid(7), "b2", GiB, exp)).toBe(true);
    expect(await registry().reserveBlob(mid(7), "b3", 1, exp)).toBe(false);
    expect(await registry().reserveBlob(mid(8), "b4", 1, exp)).toBe(true);
    await registry().releaseBlob("b1");
    expect(await registry().reserveBlob(mid(7), "b3", 1, exp)).toBe(true);
  });
});
