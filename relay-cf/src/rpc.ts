// Cross-object calls fail now and then for reasons outside the relay (an object restarting
// after a deploy, an overloaded or briefly unreachable machine). Cloudflare marks those
// errors with `retryable: true` and recommends one retry on a fresh stub, because a stub
// that saw such an error may be broken.

export function isRetryable(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { retryable?: unknown }).retryable === true;
}

// Runs call on a stub from stub() and, if it throws a retryable error, once more on a new
// stub. Callers must accept that the first attempt may have taken effect: an enqueue can
// then store the frame twice, which relay-v1 allows (delivery is at least once and clients
// deduplicate by id).
export async function retryOnce<S, T>(stub: () => S, call: (s: S) => Promise<T>): Promise<T> {
  try {
    return await call(stub());
  } catch (err) {
    if (!isRetryable(err)) throw err;
    return await call(stub());
  }
}

// A short description of err for logs: only its name and message. Callers log this and the
// op name, never the request, so frames, tokens, keys and signatures stay out of the logs.
export function describeError(err: unknown): string {
  if (err instanceof Error) return `${err.name}: ${err.message}`;
  return String(err);
}
