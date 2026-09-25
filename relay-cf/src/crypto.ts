import { AUTH_CONTEXT, HEADER_IK, HEADER_SIG, HEADER_TS, HTTP_CONTEXT } from "./protocol";

const enc = new TextEncoder();

// ---------- base64 (standard alphabet, output unpadded, input padded or unpadded) ----------

export function b64encode(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(bin).replace(/=+$/, "");
}

export function b64decode(s: string): Uint8Array {
  const body = s.replace(/=+$/, "");
  if (!/^[A-Za-z0-9+/]*$/.test(body) || body.length % 4 === 1 || s.length - body.length > 2) {
    throw new Error("invalid base64");
  }
  const padded = body + "=".repeat((4 - (body.length % 4)) % 4);
  const bin = atob(padded);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// Decodes a 32-byte Ed25519 public key; returns null on any error.
export function decodeIK(s: unknown): Uint8Array | null {
  if (typeof s !== "string") return null;
  try {
    const b = b64decode(s);
    return b.length === 32 ? b : null;
  } catch {
    return null;
  }
}

// ---------- base32 (RFC 4648 alphabet, lowercase, no padding) ----------

const B32 = "abcdefghijklmnopqrstuvwxyz234567";

export function base32(bytes: Uint8Array): string {
  let out = "";
  let bits = 0;
  let value = 0;
  for (const byte of bytes) {
    value = (value << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      out += B32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  if (bits > 0) out += B32[(value << (5 - bits)) & 31];
  return out;
}

// ---------- hashing, ids, randomness ----------

export async function sha256(data: Uint8Array | string): Promise<Uint8Array> {
  const input = typeof data === "string" ? enc.encode(data) : data;
  return new Uint8Array(await crypto.subtle.digest("SHA-256", input));
}

export function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export async function sha256Hex(data: Uint8Array | string): Promise<string> {
  return hex(await sha256(data));
}

// Mailbox id = lowercase unpadded base32 of SHA-256(ik): 52 chars. Same as core.MachineID.
export async function mailboxIdOf(ik: Uint8Array): Promise<string> {
  return base32(await sha256(ik));
}

export function randomBytes(n: number): Uint8Array {
  return crypto.getRandomValues(new Uint8Array(n));
}

export function randomToken(): string {
  return hex(randomBytes(16));
}

const CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

export function randomNameplate(): string {
  const b = randomBytes(4);
  let out = "";
  for (let i = 0; i < 4; i++) out += CROCKFORD[b[i] & 31];
  return out;
}

export function randomBlobId(): string {
  return base32(randomBytes(16)); // 26 chars
}

// Constant-time comparison: both sides are hashed first so lengths never leak.
export async function constantTimeEqual(a: string, b: string): Promise<boolean> {
  const [ha, hb] = await Promise.all([sha256(a), sha256(b)]);
  return crypto.subtle.timingSafeEqual(ha, hb);
}

// ---------- Ed25519 ----------

export async function verifyEd25519(ik: Uint8Array, msg: Uint8Array, sig: Uint8Array): Promise<boolean> {
  if (ik.length !== 32 || sig.length !== 64) return false;
  try {
    const key = await crypto.subtle.importKey("raw", ik, { name: "Ed25519" }, false, ["verify"]);
    return await crypto.subtle.verify("Ed25519", key, sig, msg);
  } catch {
    return false;
  }
}

// ---------- signing strings (C3) ----------

export function authMessage(origin: string, nonce: string): Uint8Array {
  return enc.encode(`${AUTH_CONTEXT}\n${origin}\n${nonce}`);
}

// origin is the relay's normalized auth origin; path is the raw request path (URL.pathname,
// still percent-encoded), without the query string.
export async function httpMessage(
  origin: string,
  method: string,
  path: string,
  ts: number,
  body: Uint8Array,
): Promise<Uint8Array> {
  return enc.encode(`${HTTP_CONTEXT}\n${origin}\n${method}\n${path}\n${ts}\n${await sha256Hex(body)}`);
}

const DEFAULT_PORTS: Record<string, string> = { "http:": "80", "https:": "443" };

// Normalizes an origin the way clients do (relay-v1 section 1): scheme://host[:port] with a
// lowercase host, no trailing dot, the default port omitted, IPv6 bracketed, no path.
// Input that is not a URL is returned unchanged.
export function normalizeOrigin(raw: string): string {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return raw;
  }
  const scheme = u.protocol.toLowerCase();
  const host = u.hostname.toLowerCase().replace(/\.$/, "");
  const port = u.port !== "" && u.port !== DEFAULT_PORTS[scheme] ? `:${u.port}` : "";
  return `${scheme}//${host}${port}`;
}

export interface SignedCaller {
  ik: Uint8Array;
  ikB64: string;
  mailboxId: string;
}

// Verifies X-Cravv-IK / X-Cravv-TS / X-Cravv-Sig over origin, method, path, ts (unix seconds)
// and body. origin is the relay's own normalized origin, never taken from the request headers.
// Returns null when any header is missing or malformed, the clock skew exceeds skewSeconds,
// or the signature does not verify.
export async function verifySignedRequest(
  request: Request,
  body: Uint8Array,
  origin: string,
  nowMs: number,
  skewSeconds: number,
): Promise<SignedCaller | null> {
  const ik = decodeIK(request.headers.get(HEADER_IK) ?? undefined);
  const tsRaw = request.headers.get(HEADER_TS) ?? "";
  const sigRaw = request.headers.get(HEADER_SIG) ?? "";
  if (!ik || !/^-?\d{1,15}$/.test(tsRaw)) return null;
  const ts = Number(tsRaw);
  if (Math.abs(nowMs / 1000 - ts) > skewSeconds) return null;
  let sig: Uint8Array;
  try {
    sig = b64decode(sigRaw);
  } catch {
    return null;
  }
  const path = new URL(request.url).pathname;
  const msg = await httpMessage(origin, request.method, path, ts, body);
  if (!(await verifyEd25519(ik, msg, sig))) return null;
  return { ik, ikB64: b64encode(ik), mailboxId: await mailboxIdOf(ik) };
}
