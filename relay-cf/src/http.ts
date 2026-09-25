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

// The origin clients sign for this relay, for both WebSocket auth and HTTP requests:
// PUBLIC_ORIGIN when set, otherwise the request URL's origin (which Cloudflare routes by
// hostname, so the client cannot choose it). Always normalized.
export function relayOrigin(env: Env, request: Request): string {
  return normalizeOrigin(env.PUBLIC_ORIGIN || new URL(request.url).origin);
}
