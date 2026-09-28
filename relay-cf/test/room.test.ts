import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { b64encode } from "../src/crypto";
import { Conn, frameB64, Identity, member, testEnv } from "./helpers";

async function createRoom(): Promise<{ nameplate: string; token: string }> {
  const owner = await Identity.create();
  const c = await member(owner);
  const res = await c.request({ t: "room_create" });
  c.close();
  expect(res.status).toBe("ok");
  return { nameplate: res.nameplate as string, token: res.creator_token as string };
}

async function openCreator(nameplate: string, token: string): Promise<Conn> {
  const creator = await Conn.open(`/v1/pair/${nameplate}?token=${token}`);
  expect(await creator.next()).toEqual({ t: "waiting" });
  return creator;
}

async function openJoiner(nameplate: string): Promise<Conn> {
  const joiner = await Conn.open(`/v1/pair/${nameplate}`);
  expect(await joiner.next()).toEqual({ t: "peer_joined" });
  return joiner;
}

describe("pairing rooms", () => {
  it("room_create returns a 4-char Crockford nameplate and a 32-hex creator token", async () => {
    const { nameplate, token } = await createRoom();
    expect(nameplate).toMatch(/^[0-9A-HJKMNP-TV-Z]{4}$/);
    expect(token).toMatch(/^[0-9a-f]{32}$/);
  });

  it("creator waits, joiner arrives, messages flow both ways, close burns the room", async () => {
    const { nameplate, token } = await createRoom();
    const creator = await openCreator(nameplate, token);
    const joiner = await openJoiner(nameplate.toLowerCase());
    expect(await creator.next()).toEqual({ t: "peer_joined" });

    joiner.send({ t: "msg", data: frameB64("pake-b") });
    expect(await creator.next()).toEqual({ t: "msg", data: frameB64("pake-b") });
    creator.send({ t: "msg", data: frameB64("pake-a") });
    expect(await joiner.next()).toEqual({ t: "msg", data: frameB64("pake-a") });

    joiner.close();
    expect(await creator.next()).toEqual({ t: "closed" });
    await creator.waitClosed();
    expect(creator.closeCode).toBe(1000);

    const late = await Conn.open(`/v1/pair/${nameplate}`);
    expect(await late.next()).toMatchObject({ t: "error", code: "not_found" });
  });

  it("accepts a mixed-case nameplate on the creator side", async () => {
    let room = await createRoom();
    while (!/[A-Z]/.test(room.nameplate)) room = await createRoom();
    const { nameplate, token } = room;
    const mixed = nameplate
      .split("")
      .map((ch, i) => (i % 2 === 0 ? ch.toLowerCase() : ch))
      .join("");
    const creator = await openCreator(mixed, token);
    const joiner = await openJoiner(nameplate.toLowerCase());
    expect(await creator.next()).toEqual({ t: "peer_joined" });
    joiner.close();
    creator.close();
  });

  it("buffers up to 16 creator messages and delivers them right after peer_joined", async () => {
    const { nameplate, token } = await createRoom();
    const creator = await openCreator(nameplate, token);
    for (let i = 0; i < 16; i++) creator.send({ t: "msg", data: frameB64(`early-${i}`) });
    expect(await creator.quiet(100)).toBe(true);
    const joiner = await openJoiner(nameplate);
    for (let i = 0; i < 16; i++) expect(await joiner.next()).toEqual({ t: "msg", data: frameB64(`early-${i}`) });
    expect(await creator.next()).toEqual({ t: "peer_joined" });
    creator.close();
    expect(await joiner.next()).toEqual({ t: "closed" });
  });

  it("a 17th message before the join is bad_request and ends the room", async () => {
    const { nameplate, token } = await createRoom();
    const creator = await openCreator(nameplate, token);
    for (let i = 0; i < 17; i++) creator.send({ t: "msg", data: frameB64("x") });
    expect(await creator.next()).toMatchObject({ t: "error", code: "bad_request" });
    await creator.waitClosed();
    const late = await Conn.open(`/v1/pair/${nameplate}`);
    expect(await late.next()).toMatchObject({ t: "error", code: "not_found" });
  });

  it("a joiner before the creator gets not_found and the join is not consumed", async () => {
    const { nameplate, token } = await createRoom();
    const early = await Conn.open(`/v1/pair/${nameplate}`);
    expect(await early.next()).toMatchObject({ t: "error", code: "not_found" });
    const creator = await openCreator(nameplate, token);
    const joiner = await openJoiner(nameplate);
    expect(await creator.next()).toEqual({ t: "peer_joined" });
    joiner.close();
    creator.close();
  });

  it("allows exactly one joiner and one creator", async () => {
    const { nameplate, token } = await createRoom();
    const creator = await openCreator(nameplate, token);
    const second = await Conn.open(`/v1/pair/${nameplate}?token=${token}`);
    expect(await second.next()).toMatchObject({ t: "error", code: "gone" });
    const j1 = await openJoiner(nameplate);
    expect(await creator.next()).toEqual({ t: "peer_joined" });
    const j2 = await Conn.open(`/v1/pair/${nameplate}`);
    expect(await j2.next()).toMatchObject({ t: "error", code: "gone" });
    await j2.waitClosed();
    j1.close();
    creator.close();
  });

  it("a non-msg frame is bad_request and the other side gets closed", async () => {
    const { nameplate, token } = await createRoom();
    const creator = await openCreator(nameplate, token);
    const joiner = await openJoiner(nameplate);
    await creator.next();
    joiner.send({ t: "hello" });
    expect(await joiner.next()).toMatchObject({ t: "error", code: "bad_request" });
    expect(await creator.next()).toEqual({ t: "closed" });
    await creator.waitClosed();
  });

  it("a message over 256 KiB is too_large and ends the room", async () => {
    const { nameplate, token } = await createRoom();
    const creator = await openCreator(nameplate, token);
    const joiner = await openJoiner(nameplate);
    await creator.next();
    creator.send({ t: "msg", data: b64encode(new Uint8Array(262144)) });
    expect(await joiner.next()).toMatchObject({ t: "msg" });
    creator.send({ t: "msg", data: b64encode(new Uint8Array(262145)) });
    expect(await creator.next()).toMatchObject({ t: "error", code: "too_large" });
    expect(await joiner.next()).toEqual({ t: "closed" });
  });

  it("rejects a wrong creator token and unknown nameplates", async () => {
    const { nameplate } = await createRoom();
    const bad = await Conn.open(`/v1/pair/${nameplate}?token=deadbeef`);
    expect(await bad.next()).toMatchObject({ t: "error", code: "forbidden" });
    const unknown = await Conn.open(`/v1/pair/ZZZZ`);
    expect(await unknown.next()).toMatchObject({ t: "error", code: "not_found" });
    const invalid = await Conn.open(`/v1/pair/UUUU`);
    expect(await invalid.next()).toMatchObject({ t: "error", code: "not_found" });
  });

  it("expires at the TTL alarm and tells connected sides the room is gone", async () => {
    const { nameplate, token } = await createRoom();
    const creator = await openCreator(nameplate, token);
    expect(await runDurableObjectAlarm(testEnv.ROOM.getByName(nameplate))).toBe(true);
    expect(await creator.next()).toMatchObject({ t: "error", code: "gone" });
    const late = await Conn.open(`/v1/pair/${nameplate}`);
    expect(await late.next()).toMatchObject({ t: "error", code: "not_found" });
  });
});

describe("live rooms per member", () => {
  async function fill(c: Conn): Promise<{ nameplate: string; token: string }[]> {
    const rooms: { nameplate: string; token: string }[] = [];
    for (let i = 0; i < 8; i++) {
      const res = await c.request({ t: "room_create" });
      expect(res.status).toBe("ok");
      rooms.push({ nameplate: res.nameplate as string, token: res.creator_token as string });
    }
    expect(await c.request({ t: "room_create" })).toMatchObject({ t: "res", status: "error", code: "rate_limited" });
    return rooms;
  }

  it("allows 8, then rate_limited; the cap is per member", async () => {
    const c = await member(await Identity.create());
    await fill(c);
    const other = await member(await Identity.create());
    expect((await other.request({ t: "room_create" })).status).toBe("ok");
    c.close();
    other.close();
  });

  it("a burned room frees its slot", async () => {
    const c = await member(await Identity.create());
    const [first] = await fill(c);
    const creator = await openCreator(first.nameplate, first.token);
    creator.close();
    await expect.poll(async () => (await c.request({ t: "room_create" })).status).toBe("ok");
    c.close();
  });

  it("an expired room no longer counts", async () => {
    const id = await Identity.create();
    const c = await member(id);
    await fill(c);
    await runInDurableObject(testEnv.MAILBOX.getByName(id.mailboxId), (_inst, state) => {
      state.storage.sql.exec("UPDATE room_usage SET expires_at = ? WHERE room IN (SELECT room FROM room_usage LIMIT 1)", Date.now() - 1);
    });
    expect((await c.request({ t: "room_create" })).status).toBe("ok");
    c.close();
  });
});
