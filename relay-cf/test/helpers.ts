import { env as rawEnv, exports } from "cloudflare:workers";
import { authMessage, b64encode, httpMessage, mailboxIdOf } from "../src/crypto";
import type { Env } from "../src/env";

export const ORIGIN = "https://relay.test";
export const ADMIN_TOKEN = "test-admin-token";
export const testEnv = rawEnv as unknown as Env;
const worker = (exports as unknown as { default: Fetcher }).default;

export function relayFetch(path: string, init?: RequestInit): Promise<Response> {
  return worker.fetch(`${ORIGIN}${path}`, init);
}

// A test machine identity: an Ed25519 key pair generated with WebCrypto.
export class Identity {
  private constructor(
    private readonly priv: CryptoKey,
    readonly pub: Uint8Array,
    readonly ikB64: string,
    readonly mailboxId: string,
  ) {}

  static async create(): Promise<Identity> {
    const kp = (await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"])) as CryptoKeyPair;
    const pub = new Uint8Array((await crypto.subtle.exportKey("raw", kp.publicKey)) as ArrayBuffer);
    return new Identity(kp.privateKey, pub, b64encode(pub), await mailboxIdOf(pub));
  }

  async sign(msg: Uint8Array): Promise<Uint8Array> {
    return new Uint8Array(await crypto.subtle.sign("Ed25519", this.priv, msg));
  }

  async signedHeaders(method: string, path: string, body: Uint8Array, tsOverride?: number): Promise<Record<string, string>> {
    const ts = tsOverride ?? Math.floor(Date.now() / 1000);
    const sig = await this.sign(await httpMessage(method, path, ts, body));
    return { "X-Cravv-IK": this.ikB64, "X-Cravv-TS": String(ts), "X-Cravv-Sig": b64encode(sig) };
  }
}

export type Msg = Record<string, unknown> & { t: string };

// Conn wraps a client WebSocket with an awaitable inbox of parsed frames.
export class Conn {
  private readonly inbox: Msg[] = [];
  private waiter: (() => void) | null = null;
  private rid = 0;
  closed = false;
  closeCode = 0;

  private constructor(readonly ws: WebSocket) {
    ws.addEventListener("message", (ev) => {
      this.inbox.push(JSON.parse(ev.data as string) as Msg);
      this.wake();
    });
    ws.addEventListener("close", (ev) => {
      this.closed = true;
      this.closeCode = ev.code;
      this.wake();
    });
  }

  static async open(path: string): Promise<Conn> {
    const res = await relayFetch(path, { headers: { Upgrade: "websocket" } });
    const ws = res.webSocket;
    if (!ws) throw new Error(`no websocket, status ${res.status}`);
    ws.accept();
    return new Conn(ws);
  }

  private wake(): void {
    const w = this.waiter;
    this.waiter = null;
    w?.();
  }

  // Resolves with the next frame; rejects if the socket closes first or after timeoutMs.
  async next(timeoutMs = 3000): Promise<Msg> {
    const deadline = Date.now() + timeoutMs;
    while (this.inbox.length === 0) {
      if (this.closed) throw new Error("socket closed");
      const left = deadline - Date.now();
      if (left <= 0) throw new Error("timeout waiting for frame");
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, left);
        this.waiter = () => {
          clearTimeout(timer);
          resolve();
        };
      });
    }
    return this.inbox.shift() as Msg;
  }

  // True when no frame arrives within ms.
  async quiet(ms = 300): Promise<boolean> {
    try {
      await this.next(ms);
      return false;
    } catch {
      return true;
    }
  }

  async waitClosed(timeoutMs = 3000): Promise<void> {
    const deadline = Date.now() + timeoutMs;
    while (!this.closed && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 20));
    }
    if (!this.closed) throw new Error("socket did not close");
  }

  send(frame: Record<string, unknown>): void {
    this.ws.send(JSON.stringify(frame));
  }

  // Sends a request with a fresh rid and returns the matching res frame.
  // Frames of other types that arrive meanwhile are kept in `skipped`.
  async request(frame: Record<string, unknown>, skipped: Msg[] = []): Promise<Msg> {
    const rid = `r${++this.rid}`;
    this.send({ ...frame, rid });
    for (;;) {
      const m = await this.next();
      if (m.t === "res" && m.rid === rid) return m;
      skipped.push(m);
    }
  }

  close(): void {
    try {
      this.ws.close(1000, "bye");
    } catch {
      // already closed
    }
  }
}

// Runs hello/welcome/challenge/auth and returns the connection plus the auth_ok frame.
export async function handshake(id: Identity, opts: { origin?: string; queryIk?: string } = {}): Promise<{ conn: Conn; authOk: Msg }> {
  const conn = await Conn.open(`/v1/connect?ik=${encodeURIComponent(opts.queryIk ?? id.ikB64)}`);
  conn.send({ t: "hello", versions: [1] });
  const welcome = await conn.next();
  if (welcome.t !== "welcome") throw new Error(`expected welcome, got ${JSON.stringify(welcome)}`);
  const challenge = await conn.next();
  const nonce = challenge.nonce as string;
  const sig = await id.sign(authMessage(opts.origin ?? ORIGIN, nonce));
  conn.send({ t: "auth", ik: id.ikB64, sig: b64encode(sig) });
  const authOk = await conn.next();
  return { conn, authOk };
}

// Connects and registers (admin token by default). Returns a ready connection.
export async function member(id: Identity, creds: { admin_token?: string; invite?: string } = { admin_token: ADMIN_TOKEN }): Promise<Conn> {
  const { conn, authOk } = await handshake(id);
  if (authOk.t !== "auth_ok") throw new Error(`auth failed: ${JSON.stringify(authOk)}`);
  if (!authOk.registered) {
    const res = await conn.request({ t: "register", ...creds });
    if (res.status !== "ok") throw new Error(`register failed: ${JSON.stringify(res)}`);
  }
  return conn;
}

export function frameB64(text: string): string {
  return b64encode(new TextEncoder().encode(text));
}
