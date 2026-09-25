import type { Env } from "./env";

// Exact values from the plan's Global Constraints. Every value can be lowered through an
// env var (for example in .dev.vars) so the conformance suite can exercise caps quickly.
export interface Limits {
  queueMaxFrames: number;
  queueMaxBytes: number;
  queueTtlMs: number;
  roomTtlMs: number;
  inviteTtlMs: number;
  blobTtlMs: number;
  blobQuotaBytes: number;
  requestBurst: number;
  requestRatePerSecond: number;
  maxFrameBytes: number;
  maxBlobBytes: number;
  chunkBytes: number;
  chunkOverhead: number;
  maxRoomMessageBytes: number;
  roomBufferMax: number;
  maxCreateBody: number;
  httpSkewSeconds: number;
}

export const DEFAULT_LIMITS: Limits = {
  queueMaxFrames: 10000,
  queueMaxBytes: 52428800,
  queueTtlMs: 7 * 24 * 60 * 60 * 1000,
  roomTtlMs: 10 * 60 * 1000,
  inviteTtlMs: 10 * 60 * 1000,
  blobTtlMs: 7 * 24 * 60 * 60 * 1000,
  blobQuotaBytes: 2 * 1024 * 1024 * 1024,
  requestBurst: 1000,
  requestRatePerSecond: 200,
  maxFrameBytes: 262144,
  maxBlobBytes: 104857600,
  chunkBytes: 1048576,
  chunkOverhead: 64,
  maxRoomMessageBytes: 262144,
  roomBufferMax: 16,
  maxCreateBody: 4096,
  httpSkewSeconds: 300,
};

function positiveInt(raw: string | undefined, fallback: number): number {
  if (raw === undefined || raw === "") return fallback;
  const n = Number(raw);
  return Number.isSafeInteger(n) && n > 0 ? n : fallback;
}

// Rate limits are on unless DISABLE_RATE_LIMITS is "1" (operators may relax them; relay-v1 section 7).
export function rateLimitsEnabled(env: Env): boolean {
  return env.DISABLE_RATE_LIMITS !== "1";
}

export function readLimits(env: Env): Limits {
  return {
    ...DEFAULT_LIMITS,
    queueMaxFrames: positiveInt(env.QUEUE_MAX_FRAMES, DEFAULT_LIMITS.queueMaxFrames),
    queueMaxBytes: positiveInt(env.QUEUE_MAX_BYTES, DEFAULT_LIMITS.queueMaxBytes),
    queueTtlMs: positiveInt(env.QUEUE_TTL_SECONDS, DEFAULT_LIMITS.queueTtlMs / 1000) * 1000,
    roomTtlMs: positiveInt(env.ROOM_TTL_SECONDS, DEFAULT_LIMITS.roomTtlMs / 1000) * 1000,
    inviteTtlMs: positiveInt(env.INVITE_TTL_SECONDS, DEFAULT_LIMITS.inviteTtlMs / 1000) * 1000,
    blobTtlMs: positiveInt(env.BLOB_TTL_SECONDS, DEFAULT_LIMITS.blobTtlMs / 1000) * 1000,
    blobQuotaBytes: positiveInt(env.BLOB_QUOTA_BYTES, DEFAULT_LIMITS.blobQuotaBytes),
    requestBurst: positiveInt(env.REQUEST_BURST, DEFAULT_LIMITS.requestBurst),
    requestRatePerSecond: positiveInt(env.REQUEST_RATE_PER_SECOND, DEFAULT_LIMITS.requestRatePerSecond),
  };
}
