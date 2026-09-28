import { describe, expect, it, vi } from "vitest";
import worker from "../src/index";
import type { Env } from "../src/env";
import { testEnv } from "./helpers";

describe("worker router", () => {
  it("answers an unexpected exception with a fixed 500 JSON body", async () => {
    const env: Env = {
      ...testEnv,
      IP_LIMITER: {
        limit: () => {
          throw new Error("secret internals");
        },
      } as unknown as RateLimit,
    };
    const res = await worker.fetch(new Request("https://relay.test/v1/blobs", { method: "POST" }), env);
    expect(res.status).toBe(500);
    expect(await res.json()).toEqual({ code: "internal", message: "internal error" });
  });

  it("refuses every request with a 500 naming PUBLIC_ORIGIN when it is malformed, and logs once", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      for (const bad of ["relay.example.com", "https://relay.example.com/v1", "ftp://relay.example.com"]) {
        const env: Env = { ...testEnv, PUBLIC_ORIGIN: bad };
        for (const path of ["/v1/health", "/v1/connect?ik=x", "/v1/blobs"]) {
          const res = await worker.fetch(new Request(`https://relay.test${path}`), env);
          expect(res.status).toBe(500);
          expect(await res.json()).toEqual({ code: "internal", message: "relay misconfigured: PUBLIC_ORIGIN" });
        }
      }
      expect(log).toHaveBeenCalledTimes(1);
      expect(String(log.mock.calls[0][0])).toContain("relay misconfigured: PUBLIC_ORIGIN");
    } finally {
      log.mockRestore();
    }
  });

  it("serves requests when PUBLIC_ORIGIN is a clean origin", async () => {
    const env: Env = { ...testEnv, PUBLIC_ORIGIN: "HTTPS://Relay.Test:443/" };
    const res = await worker.fetch(new Request("https://relay.test/v1/health"), env);
    expect(res.status).toBe(200);
  });
});
