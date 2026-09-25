import { chunkKey, type BlobAction } from "./blobmeta";
import { b64encode, decodeIK, randomBlobId, verifySignedRequest, type SignedCaller } from "./crypto";
import type { Env } from "./env";
import { jsonError } from "./http";
import { readLimits, type Limits } from "./limits";
import { BLOB_ID_RE, Code } from "./protocol";
import { REGISTRY_NAME } from "./registry";

type Handler = (ctx: BlobContext) => Promise<Response>;

interface BlobContext {
  env: Env;
  limits: Limits;
  caller: SignedCaller;
  body: Uint8Array;
  blobId: string;
  chunk: number;
}

interface Route {
  method: string;
  pattern: RegExp;
  handler: Handler;
}

const ROUTES: Route[] = [
  { method: "POST", pattern: /^\/v1\/blobs$/, handler: createBlob },
  { method: "PUT", pattern: /^\/v1\/blobs\/([^/]+)\/chunks\/(\d{1,6})$/, handler: putChunk },
  { method: "GET", pattern: /^\/v1\/blobs\/([^/]+)\/chunks\/(\d{1,6})$/, handler: getChunk },
  { method: "DELETE", pattern: /^\/v1\/blobs\/([^/]+)$/, handler: deleteBlob },
];

// handleBlobs serves every /v1/blobs request. Check order (relay-v1 6.3): rate limit (429, done
// by the Worker), body size (413), signature (401), membership (403), blob exists (404),
// expired (410), caller's role (403), chunk index (400), then the operation.
export async function handleBlobs(request: Request, env: Env): Promise<Response> {
  const limits = readLimits(env);
  const path = new URL(request.url).pathname;
  let route: Route | undefined;
  let m: RegExpMatchArray | null = null;
  for (const r of ROUTES) {
    m = r.method === request.method ? path.match(r.pattern) : null;
    if (m) {
      route = r;
      break;
    }
  }
  if (!route || !m) return jsonError(404, Code.NOT_FOUND, "no such blob endpoint");

  const maxBody = request.method === "PUT" ? limits.chunkBytes + limits.chunkOverhead : limits.maxCreateBody;
  const declared = Number(request.headers.get("Content-Length") ?? "0");
  if (declared > maxBody) return jsonError(413, Code.TOO_LARGE, "body too large");
  const body = await readBody(request, maxBody);
  if (!body) return jsonError(413, Code.TOO_LARGE, "body too large");

  const caller = await verifySignedRequest(request, body, Date.now(), limits.httpSkewSeconds);
  if (!caller) return jsonError(401, Code.AUTH_FAILED, "bad or missing request signature");
  // Membership lives in the caller's own Mailbox DO, so this never touches the global Registry.
  if (!(await env.MAILBOX.getByName(caller.mailboxId).isRegistered())) {
    return jsonError(403, Code.FORBIDDEN, "not a member of this relay");
  }

  const blobId = m[1] ?? "";
  if (route.handler !== createBlob && !BLOB_ID_RE.test(blobId)) {
    return jsonError(404, Code.NOT_FOUND, "no such blob");
  }
  return route.handler({ env, limits, caller, body, blobId, chunk: Number(m[2] ?? "0") });
}

// Reads the request body, counting bytes as they arrive. Returns null as soon as the body
// exceeds max bytes, without buffering the rest (Content-Length may be absent or wrong).
async function readBody(request: Request, max: number): Promise<Uint8Array | null> {
  if (!request.body) return new Uint8Array(0);
  const reader = request.body.getReader();
  const parts: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    total += value.byteLength;
    if (total > max) {
      await reader.cancel().catch(() => undefined);
      return null;
    }
    parts.push(value);
  }
  const out = new Uint8Array(total);
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.byteLength;
  }
  return out;
}

async function createBlob(ctx: BlobContext): Promise<Response> {
  let req: { size?: unknown; chunks?: unknown; recipient?: unknown };
  try {
    req = JSON.parse(new TextDecoder().decode(ctx.body));
  } catch {
    return jsonError(400, Code.BAD_REQUEST, "body must be JSON");
  }
  const { size, chunks } = req;
  const recipient = decodeIK(req.recipient);
  if (typeof size !== "number" || !Number.isSafeInteger(size) || size < 0) {
    return jsonError(400, Code.BAD_REQUEST, "size must be a non-negative integer");
  }
  if (size > ctx.limits.maxBlobBytes) return jsonError(413, Code.TOO_LARGE, "blob larger than the relay allows");
  const maxChunks = Math.max(1, Math.ceil(size / ctx.limits.chunkBytes));
  if (typeof chunks !== "number" || !Number.isSafeInteger(chunks) || chunks < 1 || chunks > maxChunks) {
    return jsonError(400, Code.BAD_REQUEST, "chunk count does not match size");
  }
  if (!recipient) return jsonError(400, Code.BAD_REQUEST, "bad recipient");

  const blobId = randomBlobId();
  const expiresAt = Date.now() + ctx.limits.blobTtlMs;
  const mailbox = ctx.env.MAILBOX.getByName(ctx.caller.mailboxId);
  const reserved = await mailbox.reserveBlob(blobId, size, expiresAt);
  if (reserved === "count") return jsonError(413, Code.TOO_LARGE, "too many live blobs");
  if (reserved === "quota") return jsonError(413, Code.TOO_LARGE, "blob storage quota exceeded");
  const registry = ctx.env.REGISTRY.getByName(REGISTRY_NAME);
  if (!(await registry.reserveStorage(blobId, size, expiresAt))) {
    await mailbox.releaseBlob(blobId);
    return jsonError(413, Code.TOO_LARGE, "relay blob storage is full");
  }
  const created = await ctx.env.BLOBMETA.getByName(blobId).create(
    blobId,
    ctx.caller.ikB64,
    b64encode(recipient),
    size,
    chunks,
    expiresAt,
  );
  if (!created) {
    await mailbox.releaseBlob(blobId);
    await registry.releaseStorage(blobId);
    return jsonError(500, Code.INTERNAL, "blob id collision");
  }
  return Response.json({ blob_id: blobId }, { status: 201 });
}

const FORBIDDEN_MESSAGE: Record<BlobAction, string> = {
  put: "only the uploader may write",
  get: "only the recipient may read",
  delete: "not a party to this blob",
};

async function authorize(ctx: BlobContext, action: BlobAction): Promise<Response | number> {
  const d = await ctx.env.BLOBMETA.getByName(ctx.blobId).authorize(ctx.caller.ikB64, action);
  if (d.status === 404) return jsonError(404, Code.NOT_FOUND, "no such blob");
  if (d.status === 410) return jsonError(410, Code.GONE, "blob expired");
  if (d.status === 403) return jsonError(403, Code.FORBIDDEN, FORBIDDEN_MESSAGE[action]);
  return d.chunks;
}

async function putChunk(ctx: BlobContext): Promise<Response> {
  const chunks = await authorize(ctx, "put");
  if (chunks instanceof Response) return chunks;
  if (ctx.chunk >= chunks) return jsonError(400, Code.BAD_REQUEST, "chunk index out of range");
  if (!(await ctx.env.BLOBMETA.getByName(ctx.blobId).recordChunk(ctx.chunk, ctx.body.length))) {
    return jsonError(413, Code.TOO_LARGE, "chunks exceed the declared size");
  }
  const key = chunkKey(ctx.blobId, ctx.chunk);
  await ctx.env.BLOBS.put(key, ctx.body);
  // A delete or expiry may have run while the bytes were in flight; BlobMeta removed the
  // chunks it knew about before this object existed. Re-check and clean up our own object so
  // nothing is left in R2 outside any quota.
  const after = await ctx.env.BLOBMETA.getByName(ctx.blobId).confirmChunk(ctx.chunk);
  if (after !== 0) {
    await ctx.env.BLOBS.delete(key);
    return after === 410 ? jsonError(410, Code.GONE, "blob expired") : jsonError(404, Code.NOT_FOUND, "no such blob");
  }
  return new Response(null, { status: 204 });
}

async function getChunk(ctx: BlobContext): Promise<Response> {
  const chunks = await authorize(ctx, "get");
  if (chunks instanceof Response) return chunks;
  if (ctx.chunk >= chunks) return jsonError(400, Code.BAD_REQUEST, "chunk index out of range");
  const obj = await ctx.env.BLOBS.get(chunkKey(ctx.blobId, ctx.chunk));
  if (!obj) return jsonError(404, Code.NOT_FOUND, "chunk not uploaded");
  return new Response(obj.body, { status: 200, headers: { "Content-Type": "application/octet-stream" } });
}

async function deleteBlob(ctx: BlobContext): Promise<Response> {
  const chunks = await authorize(ctx, "delete");
  if (chunks instanceof Response) return chunks;
  await ctx.env.BLOBMETA.getByName(ctx.blobId).destroy();
  return new Response(null, { status: 204 });
}
