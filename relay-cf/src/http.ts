import { normalizeOrigin } from "./crypto";
import type { Env } from "./env";
import { Code, failSocket, type ErrorCode } from "./protocol";

// Small HTTP and WebSocket helpers shared by the Worker router and the Durable Objects.

export function isUpgrade(request: Request): boolean {
  return request.headers.get("Upgrade")?.toLowerCase() === "websocket";
}

export function jsonError(status: number, code: ErrorCode, message: string): Response {
  return Response.json({ code, message }, { status });
}

export function notUpgrade(): Response {
  return jsonError(426, Code.BAD_REQUEST, "expected websocket upgrade");
}

// Completes the upgrade only to send a relay-v1 error frame and close, so clients see a
// protocol code instead of a bare HTTP status.
export function rejectSocket(code: ErrorCode, message: string): Response {
  const pair = new WebSocketPair();
  const [client, server] = Object.values(pair);
  server.accept();
  failSocket(server, code, message);
  return new Response(null, { status: 101, webSocket: client });
}

export const MISCONFIGURED_ORIGIN = "relay misconfigured: PUBLIC_ORIGIN";

let reportedBadOrigin = false;

// A set PUBLIC_ORIGIN that is not a clean http(s) origin would make every signature fail with
// auth_failed and hide the cause, so the Worker refuses all requests with a 500 naming the
// setting instead, and logs the problem once per isolate. Returns null when the setting is
// fine or unset.
export function misconfiguredOrigin(env: Env): Response | null {
  if (!env.PUBLIC_ORIGIN || normalizeOrigin(env.PUBLIC_ORIGIN) !== null) return null;
  if (!reportedBadOrigin) {
    reportedBadOrigin = true;
    console.error(`${MISCONFIGURED_ORIGIN} ${JSON.stringify(env.PUBLIC_ORIGIN)} is not scheme://host[:port] (http or https)`);
  }
  return jsonError(500, Code.INTERNAL, MISCONFIGURED_ORIGIN);
}

// The origin clients sign for this relay, for both WebSocket auth and HTTP requests:
// PUBLIC_ORIGIN when set, otherwise the request URL's origin (which Cloudflare routes by
// hostname, so the client cannot choose it). Always normalized. Throws when PUBLIC_ORIGIN is
// malformed; the Worker checks that with misconfiguredOrigin before routing.
export function relayOrigin(env: Env, request: Request): string {
  const origin = normalizeOrigin(env.PUBLIC_ORIGIN || new URL(request.url).origin);
  if (origin === null) throw new Error(MISCONFIGURED_ORIGIN);
  return origin;
}
