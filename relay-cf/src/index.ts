import { handleBlobs } from "./blobs";
import { b64encode, decodeIK, mailboxIdOf } from "./crypto";
import type { Env } from "./env";
import { rateLimitsEnabled } from "./limits";
import { Code, failSocket, NAMEPLATE_RE, ROUTE_IK_HEADER, ROUTE_MAILBOX_HEADER, type ErrorCode } from "./protocol";

export { BlobMeta } from "./blobmeta";
export { Mailbox } from "./mailbox";
export { Registry } from "./registry";
export { Room } from "./room";

function notUpgrade(): Response {
  return Response.json({ code: Code.BAD_REQUEST, message: "expected websocket upgrade" }, { status: 426 });
}

function isUpgrade(request: Request): boolean {
  return request.headers.get("Upgrade")?.toLowerCase() === "websocket";
}

// Completes the upgrade only to send a relay-v1 error frame, so clients see a protocol code.
function rejectSocket(code: ErrorCode, message: string): Response {
  const pair = new WebSocketPair();
  const [client, server] = Object.values(pair);
  server.accept();
  failSocket(server, code, message);
  return new Response(null, { status: 101, webSocket: client });
}

async function ipAllowed(request: Request, env: Env): Promise<boolean> {
  if (!env.IP_LIMITER || !rateLimitsEnabled(env)) return true;
  const ip = request.headers.get("CF-Connecting-IP") ?? "local";
  const { success } = await env.IP_LIMITER.limit({ key: ip });
  return success;
}

// GET /v1/connect?ik=<b64>: the ik query parameter is only a routing hint that picks the
// Mailbox DO. The DO still requires a signed challenge from that same key.
async function connect(request: Request, env: Env, url: URL): Promise<Response> {
  if (!isUpgrade(request)) return notUpgrade();
  const ik = decodeIK(url.searchParams.get("ik") ?? undefined);
  if (!ik) return rejectSocket(Code.BAD_REQUEST, "missing or invalid ik query parameter");
  const mailboxId = await mailboxIdOf(ik);
  const headers = new Headers(request.headers);
  headers.set(ROUTE_IK_HEADER, b64encode(ik));
  headers.set(ROUTE_MAILBOX_HEADER, mailboxId);
  return env.MAILBOX.getByName(mailboxId).fetch(new Request(request.url, { method: "GET", headers }));
}

// GET /v1/pair/{nameplate}[?token=]: routed to the Room DO for that nameplate.
async function pair(request: Request, env: Env, nameplate: string): Promise<Response> {
  if (!isUpgrade(request)) return notUpgrade();
  const np = nameplate.toUpperCase();
  if (!NAMEPLATE_RE.test(np)) return rejectSocket(Code.NOT_FOUND, "unknown pairing room");
  return env.ROOM.getByName(np).fetch(request);
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    const path = url.pathname;
    if (path === "/v1/health") return Response.json({ ok: true, version: 1 });
    if (!(await ipAllowed(request, env))) {
      return isUpgrade(request)
        ? rejectSocket(Code.RATE_LIMITED, "too many requests from this address")
        : Response.json({ code: Code.RATE_LIMITED, message: "too many requests" }, { status: 429 });
    }
    if (path === "/v1/connect") return connect(request, env, url);
    const pairMatch = path.match(/^\/v1\/pair\/([^/]+)$/);
    if (pairMatch) return pair(request, env, pairMatch[1]);
    if (path === "/v1/blobs" || path.startsWith("/v1/blobs/")) return handleBlobs(request, env);
    return Response.json({ code: Code.NOT_FOUND, message: "not found" }, { status: 404 });
  },
} satisfies ExportedHandler<Env>;
