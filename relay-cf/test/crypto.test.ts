import { describe, expect, it } from "vitest";
import {
  authMessage,
  b64decode,
  b64encode,
  base32,
  constantTimeEqual,
  decodeIK,
  mailboxIdOf,
  normalizeOrigin,
  randomBlobId,
  randomNameplate,
  verifyEd25519,
  verifySignedRequest,
} from "../src/crypto";
import { BLOB_ID_RE, NAMEPLATE_RE } from "../src/protocol";

// Vectors produced by Go (crypto/ed25519, seed = bytes 1..32) so the Worker is byte-compatible
// with internal/relayproto and core.MachineID.
const GO_PUB = "ebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ";
const GO_AUTH_SIG = "XcU6JZWP4i00gF6cDeHtgiX6ETNlHT4DQJ4335Zvpgb3ZNDX8n4YqquNL12nobh7Sj56yBeOE4Pr/1fgwUOCAQ";
const GO_MAILBOX_ID = "mw3am46w5weex4a4fqrc3avnub2a6knmgnk5nkjfzaprp5d2e64a";
// Signs "cravv-http-v1\nhttps://relay.test\nPOST\n/v1/blobs\n1790000000\n<sha256 hex of body>".
const GO_HTTP_SIG = "kqRo45u/cauOqMpTIyrb8bUNuhQIPLWM/RSQietZ1kUQrryEpgXUrQibqxIgHNo5jSSuF1g/hsnHFfQaaqnsDg";
const HTTP_ORIGIN = "https://relay.test";

describe("base64", () => {
  it.each([
    ["", ""],
    ["f", "Zg"],
    ["fo", "Zm8"],
    ["foo", "Zm9v"],
    ["foob", "Zm9vYg"],
  ])("encodes %j unpadded as %j", (plain, want) => {
    expect(b64encode(new TextEncoder().encode(plain))).toBe(want);
  });

  it("accepts padded and unpadded input", () => {
    expect(new TextDecoder().decode(b64decode("Zm8="))).toBe("fo");
    expect(new TextDecoder().decode(b64decode("Zm8"))).toBe("fo");
  });

  it.each(["Zm9v!", "Z", "Zm===", "Zm9v-_"])("rejects %j", (bad) => {
    expect(() => b64decode(bad)).toThrow();
  });

  it("decodeIK requires exactly 32 bytes", () => {
    expect(decodeIK(GO_PUB)?.length).toBe(32);
    expect(decodeIK("Zm9v")).toBeNull();
    expect(decodeIK(42)).toBeNull();
  });
});

describe("mailbox id", () => {
  it("is lowercase unpadded base32 of sha256(ik), 52 chars", async () => {
    expect(await mailboxIdOf(new Uint8Array(32))).toBe("mzuhvlpymk6xo3epygfy5h4oeaejofefn3rdhm4qfjmr2dk7fesq");
    expect(await mailboxIdOf(b64decode(GO_PUB))).toBe(GO_MAILBOX_ID);
  });

  it("base32 matches RFC 4648 vectors (lowercase, no padding)", () => {
    expect(base32(new TextEncoder().encode("foobar"))).toBe("mzxw6ytboi");
    expect(base32(new TextEncoder().encode("f"))).toBe("my");
  });
});

describe("ed25519", () => {
  it("verifies a Go-produced auth signature", async () => {
    const msg = authMessage("http://127.0.0.1:8787", "bm9uY2Utdg");
    expect(await verifyEd25519(b64decode(GO_PUB), msg, b64decode(GO_AUTH_SIG))).toBe(true);
  });

  it("rejects a signature over a different origin", async () => {
    const msg = authMessage("https://evil.example", "bm9uY2Utdg");
    expect(await verifyEd25519(b64decode(GO_PUB), msg, b64decode(GO_AUTH_SIG))).toBe(false);
  });

  it("rejects malformed keys and signatures without throwing", async () => {
    expect(await verifyEd25519(new Uint8Array(31), new Uint8Array(1), new Uint8Array(64))).toBe(false);
    expect(await verifyEd25519(b64decode(GO_PUB), new Uint8Array(1), new Uint8Array(10))).toBe(false);
  });
});

describe("signed HTTP requests", () => {
  const body = new TextEncoder().encode('{"size":5,"chunks":1}');
  const ts = 1790000000;
  const req = (sig: string, tsHeader = String(ts)) =>
    new Request("https://relay.test/v1/blobs", {
      method: "POST",
      headers: { "X-Cravv-IK": GO_PUB, "X-Cravv-TS": tsHeader, "X-Cravv-Sig": sig },
    });

  it("accepts a Go-signed request within the skew window", async () => {
    const caller = await verifySignedRequest(req(GO_HTTP_SIG), body, HTTP_ORIGIN, (ts + 299) * 1000, 300);
    expect(caller?.mailboxId).toBe(GO_MAILBOX_ID);
    expect(caller?.ikB64).toBe(GO_PUB);
  });

  it.each([
    ["outside skew", GO_HTTP_SIG, String(ts), (ts + 301) * 1000, body, HTTP_ORIGIN],
    ["outside skew in the future", GO_HTTP_SIG, String(ts), (ts - 301) * 1000, body, HTTP_ORIGIN],
    ["tampered body", GO_HTTP_SIG, String(ts), ts * 1000, new TextEncoder().encode('{"size":6,"chunks":1}'), HTTP_ORIGIN],
    ["bad ts header", GO_HTTP_SIG, "abc", ts * 1000, body, HTTP_ORIGIN],
    ["bad signature", GO_AUTH_SIG, String(ts), ts * 1000, body, HTTP_ORIGIN],
    ["another relay origin", GO_HTTP_SIG, String(ts), ts * 1000, body, "https://other.test"],
  ])("rejects %s", async (_name, sig, tsHeader, now, b, origin) => {
    expect(await verifySignedRequest(req(sig, tsHeader), b, origin, now, 300)).toBeNull();
  });
});

describe("origin normalization", () => {
  it.each([
    ["https://relay.test", "https://relay.test"],
    ["HTTPS://Relay.Example.COM/", "https://relay.example.com"],
    ["https://relay.example.com.:443", "https://relay.example.com"],
    ["http://relay.example.com:80", "http://relay.example.com"],
    ["https://relay.example.com:0443", "https://relay.example.com"],
    ["http://127.0.0.1:8787", "http://127.0.0.1:8787"],
    ["https://relay.example.com:8443", "https://relay.example.com:8443"],
    ["http://[::1]:80", "http://[::1]"],
    ["http://[0:0:0:0:0:0:0:1]:8787", "http://[::1]:8787"],
  ])("%s -> %s", (raw, want) => {
    expect(normalizeOrigin(raw)).toBe(want);
  });

  // The same inputs relayproto.NormalizeOrigin rejects.
  it.each([
    "",
    "relay.example.com",
    "not a url",
    "ftp://relay.example.com",
    "wss://relay.example.com",
    "https:relay.example.com",
    "https://",
    "https://.",
    "https://relay.example.com/path",
    "https://relay.example.com//",
    "https://relay.example.com/.",
    "https://relay.example.com?",
    "https://relay.example.com?q=1",
    "https://relay.example.com#frag",
    "https://user@relay.example.com",
    "https://relay.example.com:0",
    "https://relay.example.com:65536",
    "https://relay.example.com:x",
    "https://rel%61y.example.com",
    "https://relay\\example.com",
    "https://relay.example.com ",
    "https://rélay.example.com",
    "https://[::1",
  ])("rejects %j", (raw) => {
    expect(normalizeOrigin(raw)).toBeNull();
  });
});

describe("tokens and ids", () => {
  it("constantTimeEqual compares by value, any lengths", async () => {
    expect(await constantTimeEqual("abc", "abc")).toBe(true);
    expect(await constantTimeEqual("abc", "abcd")).toBe(false);
    expect(await constantTimeEqual("", "x")).toBe(false);
  });

  it("nameplates are 4 Crockford chars and blob ids are 26 base32 chars", () => {
    for (let i = 0; i < 200; i++) {
      expect(randomNameplate()).toMatch(NAMEPLATE_RE);
      expect(randomBlobId()).toMatch(BLOB_ID_RE);
    }
  });
});
