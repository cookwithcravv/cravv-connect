import type { BlobMeta } from "./blobmeta";
import type { Mailbox } from "./mailbox";
import type { Registry } from "./registry";
import type { Room } from "./room";

// Env lists every binding declared in wrangler.toml plus the ADMIN_TOKEN secret.
// PUBLIC_ORIGIN is the relay origin clients sign (relay-v1 section 1); set it in production.
// The other optional string vars override limits (see limits.ts); production leaves them unset.
export interface Env {
  MAILBOX: DurableObjectNamespace<Mailbox>;
  ROOM: DurableObjectNamespace<Room>;
  REGISTRY: DurableObjectNamespace<Registry>;
  BLOBMETA: DurableObjectNamespace<BlobMeta>;
  BLOBS: R2Bucket;
  IP_LIMITER?: RateLimit;
  ADMIN_TOKEN?: string;
  PUBLIC_ORIGIN?: string;
  QUEUE_MAX_FRAMES?: string;
  QUEUE_MAX_BYTES?: string;
  QUEUE_TTL_SECONDS?: string;
  ROOM_TTL_SECONDS?: string;
  INVITE_TTL_SECONDS?: string;
  BLOB_TTL_SECONDS?: string;
  BLOB_QUOTA_BYTES?: string;
  REQUEST_BURST?: string;
  REQUEST_RATE_PER_SECOND?: string;
  DISABLE_RATE_LIMITS?: string;
}
