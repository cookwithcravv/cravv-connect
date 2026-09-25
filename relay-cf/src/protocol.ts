// relay-v1 wire constants. Mirrors internal/relayproto (contract C3) exactly.

export const VERSION = 1;
export const AUTH_CONTEXT = "cravv-relay-auth-v1";
export const HTTP_CONTEXT = "cravv-http-v1";

export const HEADER_IK = "X-Cravv-IK";
export const HEADER_TS = "X-Cravv-TS";
export const HEADER_SIG = "X-Cravv-Sig";

// Internal header the Worker uses to hand the canonical routing IK to the Mailbox DO.
export const ROUTE_IK_HEADER = "X-Cravv-Route-IK";
export const ROUTE_MAILBOX_HEADER = "X-Cravv-Route-Mailbox";

export const Status = {
  OK: "ok",
  QUEUED: "queued",
  NOT_ALLOWED: "not_allowed",
  QUEUE_FULL: "queue_full",
  TOO_LARGE: "too_large",
  UNKNOWN_MAILBOX: "unknown_mailbox",
  RATE_LIMITED: "rate_limited",
  ERROR: "error",
} as const;
export type SendStatus =
  | typeof Status.QUEUED
  | typeof Status.NOT_ALLOWED
  | typeof Status.QUEUE_FULL
  | typeof Status.TOO_LARGE
  | typeof Status.UNKNOWN_MAILBOX
  | typeof Status.RATE_LIMITED;

export const Code = {
  BAD_REQUEST: "bad_request",
  UNSUPPORTED_VERSION: "unsupported_version",
  AUTH_FAILED: "auth_failed",
  NOT_REGISTERED: "not_registered",
  FORBIDDEN: "forbidden",
  NOT_FOUND: "not_found",
  GONE: "gone",
  RATE_LIMITED: "rate_limited",
  TOO_LARGE: "too_large",
  INTERNAL: "internal",
} as const;
export type ErrorCode = (typeof Code)[keyof typeof Code];

// WebSocket close code used after an error frame (policy violation).
export const CLOSE_POLICY = 1008;
export const CLOSE_NORMAL = 1000;

export interface Frame {
  t: string;
  [key: string]: unknown;
}

export function parseFrame(text: string): Frame | null {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof v !== "object" || v === null || Array.isArray(v)) return null;
  const f = v as Record<string, unknown>;
  if (typeof f.t !== "string") return null;
  return f as Frame;
}

export function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

export function errorFrame(code: ErrorCode, message: string): string {
  return JSON.stringify({ t: "error", code, message });
}

export interface ResFields {
  status: string;
  code?: string;
  invite?: string;
  nameplate?: string;
  creator_token?: string;
}

export function resFrame(rid: string, fields: ResFields): string {
  return JSON.stringify({ t: "res", rid, ...fields });
}

// Sends an error frame and closes. Safe to call on an already closed socket.
export function failSocket(ws: WebSocket, code: ErrorCode, message: string): void {
  try {
    ws.send(errorFrame(code, message));
  } catch {
    // socket already gone
  }
  try {
    ws.close(CLOSE_POLICY, code);
  } catch {
    // already closed
  }
}

export const MAILBOX_ID_RE = /^[a-z2-7]{52}$/;
export const NAMEPLATE_RE = /^[0-9A-HJKMNP-TV-Z]{4}$/;
export const BLOB_ID_RE = /^[a-z2-7]{26}$/;
